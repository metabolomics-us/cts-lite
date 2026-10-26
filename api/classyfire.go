package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ctslite/model"
	"ctslite/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const cfbBaseURL = "https://cfb.metabolomics.us/entities"
const wishartBaseURL = "http://classyfire.wishartlab.com/entities"
const cfbRequestTimeout = 30 * time.Second
const cfbCacheTTL = 24 * time.Hour

// Request pacing mirrors Wishart's per-IP limit, 12 requests per minute
var (
	cfbHitDelay    = 50 * time.Millisecond  // after a cache hit
	cfbMissFloor   = 500 * time.Millisecond // least time between requests that reach Wishart
	cfbSlotCap     = 10.0                   // Wishart's allowance per IP
	cfbSlotRefill  = 5 * time.Second        // Wishart refills one slot this often
	cfb429Cooldown = 5 * time.Second        // no cfb requests this long after a cfb 429, its allowance refills at the same rate

	cfbRetryDelays = []time.Duration{6 * time.Second, 10 * time.Second} // retry times for network errors (502/503 etc)
)

// 429s in a row before an inchikey is flagged as failed (direct and cfb)
const (
	cfbDirect429MaxRetries = 3
	cfb429MaxRetries       = 3 // When Wishart can't be reached
)

// cfbDownGiveUp = number of consecutive failures (502/503 etc) before giving up.
// cfbRouteDownFor, and a request whose lookups fail that often fast-fails its remaining compounds
var (
	cfbDownGiveUp   = 3
	cfbRouteDownFor = 60 * time.Second
)
const cfbUnavailableNote = "ClassyFire is currently unavailable"
const cfbNotFoundNote = "Not found in ClassyFire"
const cfbStandardBlock = "UHFFFAOYSA" // retry key with standard block if 404

// Some InChIKeys just 500, so check against known caffeine to see if ClassyFire is working, then finalize
const cfbInternalErrorNote = "ClassyFire internal error (HTTP 500)"
var cfbServiceCheckFor = 60 * time.Second

// cfbCheck = answer of a known-good key check
type cfbCheck int
const (
	cfbCheckUnknown cfbCheck = iota // only 429s, nothing classified
	cfbCheckUp                      // the known-good key classified
	cfbCheckDown                    // both routes failed outright (unreachable or 5xx)
)

var (
	cfbCheckMu     sync.Mutex
	cfbCheckUntil  time.Time // the last check's answer holds until this
	cfbCheckResult cfbCheck
)

// ask Wishart directly for the known-good key to verify ClassyFire is up
var cfbCheckProbe = func(ctx context.Context) cfbCheck {
	cfbCache.Delete(cfbHealthProbeKey) // a cached answer would check nothing
	a, ok := fetchOnce(ctx, cfbHealthProbeKey, true)
	if ok && !a.unavailable && a.res.mode == cfbRetryLimited {
		a, ok = fetchOnce(ctx, cfbHealthProbeKey, false)
	}
	switch {
	case !ok:
		return cfbCheckUnknown // client went away
	case a.unavailable || a.res.mode == cfbRetry500 || a.res.mode == cfbRetryLimited:
		return cfbCheckDown
	case a.res.mode == cfbTerminal && a.res.info != nil && a.res.info.Error == "":
		return cfbCheckUp
	}
	return cfbCheckUnknown // a 429, or the known-good key did not classify
}

// cfbServiceCheck runs the known-good key check (caffeine), lasts for cfbServiceCheckFor
func cfbServiceCheck(ctx context.Context) cfbCheck {
	cfbCheckMu.Lock()
	defer cfbCheckMu.Unlock()
	if time.Now().Before(cfbCheckUntil) {
		return cfbCheckResult
	}
	result := cfbCheckProbe(ctx)
	if ctx.Err() != nil {
		return result
	}
	cfbCheckResult = result
	cfbCheckUntil = time.Now().Add(cfbServiceCheckFor)
	switch cfbCheckResult {
	case cfbCheckUp:
		cfbServiceUp.Store(true)
	case cfbCheckDown:
		cfbServiceUp.Store(false) // classyfire down
	}
	return cfbCheckResult
}

// cfbBreaker is the 'give-up' state of each request, passed through classifyKey()
type cfbBreaker struct {
	downStreak int  // consecutive non-500 failures
	tripped    bool // cfb and Wishart down, remaining keys fast-fail
	direct     int  // keys answered by Wishart directly, for the span and summary log
}

// Enum for the cfb outcomes
type cfbOutcome int
const (
	cfbOutClassified cfbOutcome = iota // successful classification
	cfbOutNotFound                     // no classification (404 or empty)
	cfbOutFailed                       // error (unreachable, 429 give-up, 5xx, breaker)
)

// cfbTerminalOutcome maps results to cfbOutcome, (cfbOutFailed is handled by the caller)
// A cached 500 is a failure
func cfbTerminalOutcome(info *model.ClassyFireInfo) cfbOutcome {
	if info != nil && info.Error == "" {
		return cfbOutClassified
	}
	if info != nil && info.Error == cfbInternalErrorNote {
		return cfbOutFailed
	}
	return cfbOutNotFound
}

// cfbGate manages concurrent ClassyFire requests so that they are served in a round-robin style
var (
	cfbGateMu        sync.Mutex
	cfbNextAllowed   time.Time // next time a request may start
	cfbSlots         float64   // our model of Wishart's allowance for this server's IP
	cfbSlotsAt       time.Time // used to calculate cfbSlots, zero means full
	cfbCooldownUntil time.Time // stop cfb requests until this, set by a cfb 429

	cfbSkipUntil      time.Time // cfb looks down, stop until
	wishartSkipUntil  time.Time // Wishart looks down, stop until
	cfbFailStreak     int       // consecutive cfb failures (502/503 etc) across all requests
	wishartFailStreak int       // consecutive Wishart failures (502/503 etc) across all requests
)

// Each classification request gets its own child span under the classyfire span
var cfbHTTPClient = &http.Client{
	Timeout:   cfbRequestTimeout,
	Transport: telemetry.NewHTTPTransport(http.DefaultTransport, "ClassyFire"),
}
var wishartHTTPClient = &http.Client{
	Timeout:   cfbRequestTimeout,
	Transport: telemetry.NewHTTPTransport(http.DefaultTransport, "Wishart ClassyFire"),
}
var cfbCache sync.Map // in-memory cache of classifications, cfbCacheTTL lifetime
type cfbCacheEntry struct {
	info    *model.ClassyFireInfo
	expires time.Time
}

func cfbCacheLookup(inchikey string) (*model.ClassyFireInfo, bool) {
	if v, ok := cfbCache.Load(inchikey); ok {
		entry := v.(cfbCacheEntry)
		if time.Now().Before(entry.expires) {
			return entry.info, true
		}
		cfbCache.Delete(inchikey)
	}
	return nil, false
}

// cfbActiveRequests = how many concurrent requests are using ClassyFire
// cfbQueueDepth() = atomic read of cfbActiveRequests, avoids race condition
var cfbActiveRequests int64
func cfbEnterQueue() { atomic.AddInt64(&cfbActiveRequests, 1) }
func cfbLeaveQueue() { atomic.AddInt64(&cfbActiveRequests, -1) }
func cfbQueueDepth() int64 { return atomic.LoadInt64(&cfbActiveRequests) }

// cfbServiceUp = last known reachability of the ClassyFire service, updated by a
// periodic background probe and by live request outcomes
var cfbServiceUp atomic.Bool

func init() { cfbServiceUp.Store(true) }

const (
	cfbHealthProbeInterval = 180 * time.Second
	cfbHealthProbeKey = "RYYVLZVUVIJVGH-UHFFFAOYSA-N" // caffeine
	cfbHealthProbeTimeout = 15 * time.Second
)

// cfbHealthProbe performs a reachability check, it's a var so tests can mock it
var cfbHealthProbe = func() bool {
	return probeClassyFire(cfbBaseURL) || probeClassyFire(wishartBaseURL)
}

// probeClassyFire checks one ClassyFire endpoint
func probeClassyFire(base string) bool {
	status := probeStatus(base)
	return status != 0 && status < http.StatusInternalServerError
}

// probeStatus asks base for the known-good key and returns the HTTP status, 0 when unreachable
func probeStatus(base string) int {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s.json", base, cfbHealthProbeKey), nil)
	if err != nil {
		return 0
	}
	// Cache-Control: no-cache forces cfb to revalidate against the real classyfire API
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{Timeout: cfbHealthProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0 // network error or timeout, the service is unreachable
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// StartClassyFireHealthCheck probes ClassyFire on startup and then intermittently
func StartClassyFireHealthCheck(ctx context.Context) {
	// Export the current reachability as an observable gauge for the uptime timeline
	telemetry.RegisterClassyFireServiceGauge(func() bool { return cfbServiceUp.Load() })
	go func() {
		check := func() { cfbServiceUp.Store(cfbHealthProbe()) }
		check()
		ticker := time.NewTicker(cfbHealthProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}

// ClassyFireStatus reports whether the ClassyFire service is currently reachable
func ClassyFireStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]bool{"up": cfbServiceUp.Load()}); err != nil {
		log.Printf("Failed to encode ClassyFire status response: %v", err)
	}
}

// cfbRetryMode describes the outcome of a single lookup attempt and how the caller should retry
type cfbRetryMode int
const (
	cfbTerminal         cfbRetryMode = iota // 200, 404, or empty -> no retry
	cfbRetryLimited                         // network error or non-500 5xx -> bounded retries, counts toward 'down'
	cfbRetryRateLimited                     // 429 -> from cfb ask Wishart directly, from Wishart wait for a slot and retry, give up after cfbDirect429MaxRetries
	cfbRetry500                             // HTTP 500 -> from cfb ask Wishart directly, from both fail the key and probe the next keys
)

// cfbFetch = result of an InChIKey lookup 
type cfbFetch struct {
	info     *model.ClassyFireInfo // terminal result (classification or error)
	cacheHit bool                  // response had X-Cache: HIT
	mode     cfbRetryMode          // how to retry this key
	errMsg   string                // user-facing message for a transient failure
}

// cfbNode = node in the ClassyFire API response, the name field is the value we want
type cfbNode struct {
	Name string `json:"name"`
}

// cfbAPIResponse = response from ClassyFire API
type cfbAPIResponse struct {
	Kingdom      *cfbNode `json:"kingdom"`
	Superclass   *cfbNode `json:"superclass"`
	Class        *cfbNode `json:"class"`
	Subclass     *cfbNode `json:"subclass"`
	DirectParent *cfbNode `json:"direct_parent"`
	Description  string   `json:"description"`
}

// variables so tests mock them
var classyFireFetcher = defaultClassyFireFetcher
var wishartFetcher = defaultWishartFetcher

func defaultClassyFireFetcher(ctx context.Context, inchikey string) (cfbFetch, error) {
	return fetchClassification(ctx, cfbHTTPClient, cfbBaseURL, inchikey)
}

// Every Wishart answer counts against our allowance
func defaultWishartFetcher(ctx context.Context, inchikey string) (cfbFetch, error) {
	return fetchClassification(ctx, wishartHTTPClient, wishartBaseURL, inchikey)
}

// fetchClassification looks up one InChIKey at base, cfb and Wishart answer in the same format
func fetchClassification(ctx context.Context, client *http.Client, base, inchikey string) (cfbFetch, error) {
	// Second cache check in case the same InChIKey was fetched while we waited for the gate
	if info, ok := cfbCacheLookup(inchikey); ok {
		return cfbFetch{info: info, cacheHit: true}, nil
	}

	// Build the request. The context carries the classyfire
	// span and a client disconnect cancels the call
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s.json", base, inchikey), nil)
	if err != nil {
		return cfbFetch{}, fmt.Errorf("building request: %w", err)
	}

	// Make the request to cfb or Wishart
	resp, err := client.Do(req)
	if err != nil {
		// network/timeout errors are transient: retry a bounded number of times
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"},
			fmt.Errorf("fetching classyfire: %w", err)
	}
	defer resp.Body.Close()

	cacheHit := resp.Header.Get("X-Cache") == "HIT"

	if resp.StatusCode == http.StatusNotFound {
		// 404 not found in ClassyFire db, terminal outcome
		info := &model.ClassyFireInfo{Error: cfbNotFoundNote}
		cfbCache.Store(inchikey, cfbCacheEntry{info, time.Now().Add(cfbCacheTTL)})
		return cfbFetch{info: info, cacheHit: cacheHit}, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Rate limited, pause queue before retry
		return cfbFetch{cacheHit: cacheHit, mode: cfbRetryRateLimited,
				errMsg: "ClassyFire rate limited (HTTP 429)"},
			fmt.Errorf("classyfire HTTP 429")
	}
	if resp.StatusCode == http.StatusInternalServerError {
		// Some InChIKeys consistently return HTTP 500 even when the service is healthy,
		// so a 500 is retried per key and then confirmed by probing the next keys
		return cfbFetch{cacheHit: cacheHit, mode: cfbRetry500,
				errMsg: cfbInternalErrorNote},
			fmt.Errorf("classyfire HTTP 500")
	}
	if resp.StatusCode != http.StatusOK {
		// Other server errors (502, 503 etc)
		return cfbFetch{cacheHit: cacheHit, mode: cfbRetryLimited,
				errMsg: fmt.Sprintf("ClassyFire unavailable (HTTP %d)", resp.StatusCode)},
			fmt.Errorf("classyfire HTTP %d", resp.StatusCode)
	}

	var r cfbAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire response unreadable"},
			fmt.Errorf("decoding classyfire response: %w", err)
	}

	info := &model.ClassyFireInfo{Description: r.Description}
	if r.Kingdom != nil {
		info.Kingdom = r.Kingdom.Name
	}
	if r.Superclass != nil {
		info.Superclass = r.Superclass.Name
	}
	if r.Class != nil {
		info.Class = r.Class.Name
	}
	if r.Subclass != nil {
		info.Subclass = r.Subclass.Name
	}
	if r.DirectParent != nil {
		info.DirectParent = r.DirectParent.Name
	}

	// A body with no classification (e.g. {} or description-only) means the compound is unclassified
	if info.Kingdom == "" && info.Superclass == "" && info.Class == "" &&
		info.Subclass == "" && info.DirectParent == "" {
		info = &model.ClassyFireInfo{Error: "No classification available"}
	}

	cfbCache.Store(inchikey, cfbCacheEntry{info, time.Now().Add(cfbCacheTTL)})
	return cfbFetch{info: info, cacheHit: cacheHit}, nil
}

// sleepCtx waits for d or until ctx is cancelled, whichever comes first
// It returns ctx.Err() if the context is (or becomes) done, else nil
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cfbAttempt = outcome of a lookup, cfb possibly followed by Wishart directly
type cfbAttempt struct {
	res         cfbFetch // final result
	direct      bool     // res came from Wishart directly
	unavailable bool     // cfb and Wishart are both skipped as down, nothing was sent
}

// cfbNoteRoute counts consecutive failures of one route and skips it for cfbRouteDownFor (seconds) once it
// seems down. Any other answer, 429 and 500 included, means the route is up
func cfbNoteRoute(name string, mode cfbRetryMode, streak *int, skipUntil *time.Time, end time.Time) {
	if mode != cfbRetryLimited {
		*streak = 0
		return
	}
	*streak++
	if *streak >= cfbDownGiveUp {
		*streak = 0
		*skipUntil = end.Add(cfbRouteDownFor)
		log.Printf("%s appears down after %d consecutive failures; skipping it for %s", name, cfbDownGiveUp, cfbRouteDownFor)
	}
}

// cfbRefillSlots adds the slots earned since cfbSlotsAt
func cfbRefillSlots(now time.Time) {
	if cfbSlotsAt.IsZero() || cfbSlotRefill <= 0 {
		cfbSlots = cfbSlotCap
	} else {
		cfbSlots = min(cfbSlotCap, cfbSlots+float64(now.Sub(cfbSlotsAt))/float64(cfbSlotRefill))
	}
	cfbSlotsAt = now
}

// cfbSettle sets the gap after a request, a cache hit return their slot to the pool
func cfbSettle(res cfbFetch, end time.Time) {
	if res.cacheHit {
		cfbSlots = min(cfbSlotCap, cfbSlots+1)
		cfbNextAllowed = end.Add(cfbHitDelay)
		return
	}
	cfbNextAllowed = end.Add(cfbMissFloor)
}

// Try cfb first, then Wishart directly if cfb fails to respond.
func fetchOnce(ctx context.Context, key string, preferDirect bool) (a cfbAttempt, ok bool) {
	cfbGateMu.Lock()
	defer cfbGateMu.Unlock()

	// Wait for a slot
	var useCFB, wishartUp bool
	for {
		now := time.Now()
		cfbRefillSlots(now)
		wait := cfbNextAllowed.Sub(now)
		if cfbSlots < 1 {
			wait = max(wait, time.Duration((1-cfbSlots)*float64(cfbSlotRefill)))
		}

		cfbUp := !now.Before(cfbSkipUntil)
		wishartUp = !now.Before(wishartSkipUntil)
		if !cfbUp && !wishartUp {
			return cfbAttempt{unavailable: true}, true
		}
		cooling := now.Before(cfbCooldownUntil)
		useCFB = cfbUp && !cooling && !(preferDirect && wishartUp)
		if !useCFB && !wishartUp {
			wait = max(wait, cfbCooldownUntil.Sub(now)) // only cfb is up and it is cooling down
		}

		if wait <= 0 {
			break
		}
		if sleepCtx(ctx, wait) != nil {
			return cfbAttempt{}, false
		}
	}
	cfbSlots--

	var cfbRes cfbFetch
	if useCFB {
		res, _ := classyFireFetcher(ctx, key)
		end := time.Now()
		if ctx.Err() != nil {
			return cfbAttempt{}, false
		}
		cfbNoteRoute("cfb", res.mode, &cfbFailStreak, &cfbSkipUntil, end)
		if res.mode == cfbRetryRateLimited {
			cfbCooldownUntil = end.Add(cfb429Cooldown)
		}
		if res.mode == cfbTerminal || !wishartUp {
			cfbSettle(res, end)
			a.res = res
			return a, true
		}
		log.Printf("cfb: %s for %s; trying Wishart directly", res.errMsg, key)
		cfbRes = res
	}

	// Wishart directly, with the slot already taken
	res, _ := wishartFetcher(ctx, key)
	end := time.Now()
	if ctx.Err() != nil {
		return cfbAttempt{}, false
	}
	cfbNoteRoute("Wishart", res.mode, &wishartFailStreak, &wishartSkipUntil, end)
	if res.mode == cfbRetryRateLimited {
		cfbSlots, cfbSlotsAt = 0, end
	}
	cfbSettle(res, end)
	if useCFB && res.mode == cfbRetryLimited {
		// Wishart could not be reached, which says nothing about the key, so cfb's answer decides the retry.
		// Any answer from Wishart, a 500 included, is about the key and stands
		log.Printf("Wishart: %s for %s; keeping cfb's answer", res.errMsg, key)
		a.res = cfbRes
		return a, true
	}
	a.res, a.direct = res, true
	return a, true
}

const cfbMaxMatchesPerQuery = 1
const cfbCappedNote = "Only the top hit of each query is classified for latency considerations"

// Determine the list of InChIKeys to be classified
func classifiableKeys(results []*model.SingleResult) []string {
	seen := make(map[string]bool)
	var keys []string
	for _, r := range results {
		for i, c := range r.Matches {
			if i >= cfbMaxMatchesPerQuery {
				c.ClassyFire = &model.ClassyFireInfo{Error: cfbCappedNote}
				continue
			}
			if !seen[c.InChIKey] {
				seen[c.InChIKey] = true
				keys = append(keys, c.InChIKey)
			}
		}
	}
	return keys
}

// cfbKeyChan feeds a fixed key list to streamClassyFire
func cfbKeyChan(keys []string) <-chan string {
	ch := make(chan string, len(keys))
	for _, k := range keys {
		ch <- k
	}
	close(ch)
	return ch
}

// Perform requests for each InChIKey received on keys until the channel is closed
func streamClassyFire(ctx context.Context, keys <-chan string, onResult func(key string, info *model.ClassyFireInfo)) {
	start := time.Now()
	ctx, span := telemetry.StartSpan(ctx, "classyfire")
	br := &cfbBreaker{}
	var received, completed, misses, failures int
	cancelled := false
	// Deferred so outcomes are still counted when the client cancels mid-request
	defer func() {
		telemetry.RecordClassyFireOutcomes(ctx, completed, misses, failures)
		span.SetAttributes(
			attribute.Int("classyfire.keys", received),
			attribute.Int("classyfire.classified", completed),
			attribute.Int("classyfire.not_found", misses),
			attribute.Int("classyfire.failed", failures),
			attribute.Int("classyfire.direct", br.direct),
			attribute.Bool("classyfire.breaker_tripped", br.tripped),
		)
		if br.tripped {
			span.SetStatus(codes.Error, "ClassyFire circuit breaker tripped")
		}
		span.End()
	}()
	for key := range keys {
		received++
		// Keep draining after a cancel so a producer still sending never blocks
		if ctx.Err() != nil {
			if !cancelled {
				cancelled = true
				log.Printf("ClassyFire request cancelled by client; stopping after %d compounds", received-1)
			}
			continue
		}

		info, outcome := classifyKey(ctx, key, br)
		switch outcome {
		case cfbOutClassified:
			completed++
		case cfbOutNotFound:
			misses++
		case cfbOutFailed:
			failures++
		}
		onResult(key, info)
	}

	if completed+misses+failures == 0 {
		return
	}
	summary := fmt.Sprintf("%d classifications completed with ClassyFire", completed)
	if misses > 0 {
		summary += fmt.Sprintf(", %d misses", misses)
	}
	if failures > 0 {
		summary += fmt.Sprintf(", %d failures", failures)
	}
	if br.direct > 0 {
		summary += fmt.Sprintf(", %d answered by Wishart directly", br.direct)
	}
	log.Printf("%s in %s", summary, time.Since(start).Round(time.Millisecond))
}

// ok is false when the key already has the standard block, so a standard key is never retried
func cfbStandardKey(key string) (string, bool) {
	parts := strings.Split(key, "-")
	if len(parts) != 3 || parts[1] == cfbStandardBlock {
		return "", false
	}
	return parts[0] + "-" + cfbStandardBlock + "-" + parts[2], true
}

// Resolves one key: result, bounded retries, give up, or cancellation
func classifyKey(ctx context.Context, key string, br *cfbBreaker) (info *model.ClassyFireInfo, outcome cfbOutcome) {
	// Check our cache first
	if cached, ok := cfbCacheLookup(key); ok {
		return cached, cfbTerminalOutcome(cached)
	}

	if br.tripped {
		// fail fast
		return &model.ClassyFireInfo{Error: cfbUnavailableNote}, cfbOutFailed
	}

	info, outcome, direct := lookupKey(ctx, key, br, false)
	if !direct || info == nil || info.Error != cfbNotFoundNote {
		return info, outcome
	}

	// A 404 from Wishart tries the same structure without stereo or isotope layers
	std, ok := cfbStandardKey(key)
	if !ok {
		return info, outcome
	}
	stdInfo, found := cfbCacheLookup(std)
	stdOutcome := cfbTerminalOutcome(stdInfo)
	if !found {
		stdInfo, stdOutcome, _ = lookupKey(ctx, std, br, true)
	}
	switch stdOutcome {
	case cfbOutClassified:
		cfbCache.Store(key, cfbCacheEntry{stdInfo, time.Now().Add(cfbCacheTTL)})
		return stdInfo, stdOutcome
	case cfbOutFailed:
		// The fallback never got an answer, so the cached 404 for the original key is not final
		cfbCache.Delete(key)
		return stdInfo, stdOutcome
	}
	return info, outcome
}

// lookupKey runs the gated lookups for one key until a final answer, a give up, or cancellation
func lookupKey(ctx context.Context, key string, br *cfbBreaker, preferDirect bool) (info *model.ClassyFireInfo, outcome cfbOutcome, direct bool) {
	var cfb429, direct429, transient int
	for {
		a, ok := fetchOnce(ctx, key, preferDirect)
		if !ok {
			return nil, cfbOutFailed, false // ctx cancelled mid-fetch
		}
		if a.unavailable {
			// cfb and Wishart both look down, fail fast
			br.tripped = true
			cfbServiceUp.Store(false)
			log.Printf("ERROR: cfb and Wishart both appear down; failing remaining compounds for this request")
			return &model.ClassyFireInfo{Error: cfbUnavailableNote}, cfbOutFailed, false
		}
		res := a.res

		// Generic fallback message so that we don't ever return a blank error
		msg := res.errMsg
		if msg == "" {
			msg = "ClassyFire lookup failed"
		}

		switch res.mode {
		case cfbTerminal:
			br.downStreak = 0
			if !res.cacheHit { // only consider classyfire up if it was not a cfb cache hit
				cfbServiceUp.Store(true)
			}
			if a.direct {
				br.direct++
			}
			return res.info, cfbTerminalOutcome(res.info), a.direct

		case cfbRetryRateLimited:
			br.downStreak = 0 // 429 means the service is up
			if a.direct {
				direct429++
				if direct429 >= cfbDirect429MaxRetries {
					log.Printf("Wishart still rate limited for %s after %d retries; giving up", key, direct429)
					return &model.ClassyFireInfo{Error: msg}, cfbOutFailed, true
				}
				log.Printf("Wishart rate limited (429) for %s; retrying after the next refill (%d/%d)", key, direct429, cfbDirect429MaxRetries)
				preferDirect = true
			} else {
				// Only reached while Wishart is skipped, the gate waits out cfb's cooldown
				cfb429++
				if cfb429 >= cfb429MaxRetries {
					log.Printf("cfb still rate limited for %s after %d retries; giving up", key, cfb429)
					return &model.ClassyFireInfo{Error: msg}, cfbOutFailed, false
				}
				log.Printf("cfb rate limited (429) for %s with Wishart down; retrying after the cooldown (%d/%d)", key, cfb429, cfb429MaxRetries)
			}

		case cfbRetry500:
			// 500 from both cfb and direct is a final answer for that key (failure)
			// Try caffeine to assert ClassyFire is up before finalizing
			br.downStreak = 0
			info := &model.ClassyFireInfo{Error: msg}
			switch cfbServiceCheck(ctx) {
			case cfbCheckDown:
				br.tripped = true
				log.Printf("ERROR: ClassyFire HTTP 500 for %s and the known-good key check failed; failing remaining compounds for this request", key)
			case cfbCheckUp:
				// Only Wishart's own 500 is cached, a cfb-only 500 could be cfb failing on its own
				if a.direct {
					cfbCache.Store(key, cfbCacheEntry{info, time.Now().Add(cfbCacheTTL)})
				}
				log.Printf("ClassyFire HTTP 500 for %s while the known-good key classifies; failing this key (cached: %v)", key, a.direct)
			default:
				log.Printf("ClassyFire HTTP 500 for %s and the known-good key check only got 429s; failing this key uncached", key)
			}
			return info, cfbOutFailed, a.direct

		default: // cfbRetryLimited, service down
			br.downStreak++
			if br.downStreak >= cfbDownGiveUp {
				// cfb and Wishart look down, fail fast
				br.tripped = true
				cfbServiceUp.Store(false)
				log.Printf("ERROR: ClassyFire appears down after %d consecutive failures; failing remaining compounds for this request", br.downStreak)
				return &model.ClassyFireInfo{Error: msg}, cfbOutFailed, a.direct
			}
			if transient >= len(cfbRetryDelays) {
				log.Printf("ClassyFire lookup failed for %s after %d attempts: %s", key, transient+1, msg)
				return &model.ClassyFireInfo{Error: msg}, cfbOutFailed, a.direct
			}
			if sleepCtx(ctx, cfbRetryDelays[transient]) != nil {
				return nil, cfbOutFailed, false // ctx cancelled during backoff
			}
			transient++
		}
	}
}

// The non-streaming ClassyFire call (plain JSON and CSV responses)
// ctx cancels the work if the client disconnects
func enrichWithClassyFire(ctx context.Context, results []*model.SingleResult) {
	keys := classifiableKeys(results)
	if len(keys) == 0 {
		return
	}

	// Count toward the queue depth so frontend counts these as contending requests too
	cfbEnterQueue()
	defer cfbLeaveQueue()

	// Stream results into map and wait until completed before returning result
	cfMap := make(map[string]*model.ClassyFireInfo, len(keys))
	streamClassyFire(ctx, cfbKeyChan(keys), func(key string, info *model.ClassyFireInfo) {
		cfMap[key] = info
	})

	for _, r := range results {
		for _, c := range r.Matches {
			if c.ClassyFire != nil {
				continue // already capped by classifiableKeys
			}
			if info, ok := cfMap[c.InChIKey]; ok {
				c.ClassyFire = info
			}
		}
	}
}
