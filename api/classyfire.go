package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"ctslite/model"
	"ctslite/telemetry"
)

const cfbBaseURL = "https://cfb.metabolomics.us/entities"
const cfbRequestTimeout = 30 * time.Second // per single GET
const cfbCacheTTL = 24 * time.Hour

// Adaptive request pacing to adhere to rate limits
var (
	cfbHitDelay     = 50 * time.Millisecond   // cfb cache hit (X-Cache: HIT)
	cfbBurstGap     = 2200 * time.Millisecond // cfb cache miss during burst
	cfbSteadyGap    = 4500 * time.Millisecond // cfb cache miss once throttled
	cfb429Pause     = 6 * time.Second         // pause after a 429
	cfb429PauseLong = 15 * time.Second        // longer pause after multiple 429s
	cfbIdleReset    = 30 * time.Second        // idle until burst refill

	cfbRetryDelays = []time.Duration{6 * time.Second, 10 * time.Second} // retry times for non-429 errors
)

// cfb429StreakLong = number of consecutive 429s before we increase pausetime
// cfb429MaxRetries = limits 429s before an inchikey is flagged as failed
const (
	cfb429StreakLong = 3
	cfb429MaxRetries = 5
)

// cfbDownGiveUp = number of consecutive 5xx failures before fast-failing remaining compounds 
// 429 or 200 resets the counter. Counter is per-request.
var cfbDownGiveUp = 3
const cfbUnavailableNote = "ClassyFire is currently unavailable"

// cfbBreaker is the 'give-up' state of each request, passed through classifyKey()
type cfbBreaker struct {
	downStreak int  // consecutive failures during this request
	tripped    bool // cfb down, remaining keys fast-fail
}

// Enum for the outcomes of CFB
type cfbOutcome int
const (
	cfbOutClassified cfbOutcome = iota // successful classification
	cfbOutNotFound                     // no classification (404 or empty)
	cfbOutFailed                       // error (unreachable, 429 give-up, 5xx, breaker)
)

// cfbTerminalOutcome maps results to cfbOutcome, failures (cfbOutFailed) are handled by the caller
func cfbTerminalOutcome(info *model.ClassyFireInfo) cfbOutcome {
	if info != nil && info.Error == "" {
		return cfbOutClassified
	}
	return cfbOutNotFound
}

// cfbGate manages concurrent ClassyFire requests so that they are served in a fair, round-robin style
// cfbNextAllowed is the next time another cfb request may start
var (
	cfbGateMu      sync.Mutex
	cfbNextAllowed time.Time
	cfbMissGap     = cfbBurstGap
	cfb429Streak   int
	cfbLastReqAt   time.Time
)

var cfbHTTPClient = &http.Client{Timeout: cfbRequestTimeout}
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

// cfbRetryMode describes the outcome of a single lookup attempt and how the caller should retry
type cfbRetryMode int
const (
	cfbTerminal         cfbRetryMode = iota // 200, 404, or empty -> no retry
	cfbRetryLimited                         // 5xx error -> bounded number of retries (per key)
	cfbRetryRateLimited                     // 429 -> pause entire queue, retry, give up after cfb429MaxRetries
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

// classyFireFetcher is a variable so tests mock it
var classyFireFetcher = defaultClassyFireFetcher

func defaultClassyFireFetcher(inchikey string) (cfbFetch, error) {
	// Second cache check in case the same InChIKey was fetched while we waited for the gate
	if info, ok := cfbCacheLookup(inchikey); ok {
		return cfbFetch{info: info, cacheHit: true}, nil
	}

	// Build the request
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s.json", cfbBaseURL, inchikey), nil)
	if err != nil {
		return cfbFetch{}, fmt.Errorf("building request: %w", err)
	}

	// Make the request to CFB
	resp, err := cfbHTTPClient.Do(req)
	if err != nil {
		// network/timeout errors are transient: retry a bounded number of times
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"},
			fmt.Errorf("fetching classyfire: %w", err)
	}
	defer resp.Body.Close()

	cacheHit := resp.Header.Get("X-Cache") == "HIT"

	if resp.StatusCode == http.StatusNotFound {
		// 404 not found in ClassyFire db, terminal outcome
		info := &model.ClassyFireInfo{Error: "Not found in ClassyFire"}
		cfbCache.Store(inchikey, cfbCacheEntry{info, time.Now().Add(cfbCacheTTL)})
		return cfbFetch{info: info, cacheHit: cacheHit}, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Rate limited, pause queue before retry
		return cfbFetch{cacheHit: cacheHit, mode: cfbRetryRateLimited,
				errMsg: "ClassyFire rate limited (HTTP 429)"},
			fmt.Errorf("classyfire HTTP 429")
	}
	if resp.StatusCode != http.StatusOK {
		// Other server errors (5xx etc)
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

// fetchOnce performs a single gated lookup. It acquires the global cfb gate,
// applies the adaptive pacing (resuming the fast burst pace if the queue has been
// idle, then waiting out the gap left by the previous request), makes one
// attempt, and sets the gap for the next request from the outcome: a cache hit
// paces fast, a cache miss at the current miss gap, and a 429 settles the miss
// gap to the sustained rate and pauses the whole queue (longer once 429s pile
// up). Any non-429 outcome clears the 429 streak. ok is false if ctx was
// cancelled.
func fetchOnce(ctx context.Context, key string) (res cfbFetch, ok bool) {
	cfbGateMu.Lock()
	defer cfbGateMu.Unlock()

	// Check if it's been 30s since the last request, if so reset the miss gap
	if time.Since(cfbLastReqAt) > cfbIdleReset {
		cfbMissGap = cfbBurstGap
		cfb429Streak = 0
	}

	// Wait after previous request, globally
	if sleepCtx(ctx, time.Until(cfbNextAllowed)) != nil {
		return cfbFetch{}, false
	}

	res, _ = classyFireFetcher(key)
	end := time.Now()
	cfbLastReqAt = end
	if ctx.Err() != nil {
		return cfbFetch{}, false
	}

	switch {
	case res.mode == cfbRetryRateLimited:
		cfb429Streak++
		cfbMissGap = cfbSteadyGap
		pause := cfb429Pause
		if cfb429Streak >= cfb429StreakLong {
			pause = cfb429PauseLong
		}
		cfbNextAllowed = end.Add(pause)
	case res.cacheHit:
		cfb429Streak = 0
		cfbNextAllowed = end.Add(cfbHitDelay)
	default:
		cfb429Streak = 0
		cfbNextAllowed = end.Add(cfbMissGap)
	}
	return res, true
}

const cfbMaxMatchesPerQuery = 3
const cfbCappedNote = "Only the top 3 hits of each query are classified for latency considerations"

// Determine the list of InChIKeys to be classified. Ignore matches past the top 3 of a single query (attach capped note)
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

// Perform requests for each InChIKey in keys
func streamClassyFire(ctx context.Context, keys []string, onResult func(key string, info *model.ClassyFireInfo)) {
	start := time.Now()
	br := &cfbBreaker{}
	var completed, misses, failures int
	// Deferred so outcomes are still counted when the client cancels mid-request
	defer func() {
		telemetry.RecordClassyFireOutcomes(ctx, completed, misses, failures)
	}()
	for i, key := range keys {
		if ctx.Err() != nil {
			log.Printf("ClassyFire request cancelled by client; stopping after %d of %d compounds", i, len(keys))
			return
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
	log.Printf("%s in %s", summary, time.Since(start).Round(time.Millisecond))
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

	var rateLimited, transient int
	for {
		res, ok := fetchOnce(ctx, key)
		if !ok {
			return nil, cfbOutFailed // ctx cancelled mid-fetch
		}

		// Generic fallback message so that we don't ever return a blank error
		msg := res.errMsg
		if msg == "" {
			msg = "ClassyFire lookup failed"
		}

		switch res.mode {
		case cfbTerminal:
			br.downStreak = 0
			return res.info, cfbTerminalOutcome(res.info)

		case cfbRetryRateLimited:
			br.downStreak = 0 // 429 means cfb is up
			rateLimited++
			if rateLimited >= cfb429MaxRetries {
				log.Printf("ClassyFire still rate limited for %s after %d retries; giving up", key, rateLimited)
				return &model.ClassyFireInfo{Error: msg}, cfbOutFailed
			}
			log.Printf("ClassyFire rate limited (429) for %s; pausing and retrying (%d/%d)", key, rateLimited, cfb429MaxRetries)

		default: // cfbRetryLimited, service down
			br.downStreak++
			if br.downStreak >= cfbDownGiveUp {
				// cfb looks down, fail fast
				br.tripped = true
				log.Printf("ERROR: ClassyFire appears down after %d consecutive failures; failing remaining compounds for this request", br.downStreak)
				return &model.ClassyFireInfo{Error: msg}, cfbOutFailed
			}
			if transient >= len(cfbRetryDelays) {
				log.Printf("ClassyFire lookup failed for %s after %d attempts: %s", key, transient+1, msg)
				return &model.ClassyFireInfo{Error: msg}, cfbOutFailed
			}
			if sleepCtx(ctx, cfbRetryDelays[transient]) != nil {
				return nil, cfbOutFailed // ctx cancelled during backoff
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
	streamClassyFire(ctx, keys, func(key string, info *model.ClassyFireInfo) {
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
