package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ctslite/model"
	"ctslite/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const pcbBaseURL = "https://pubchem.ncbi.nlm.nih.gov/rest/pug"
const pcbPropertyList = "InChIKey,InChI,SMILES,ExactMass,LiteratureCount,PatentCount,AnnotationTypeCount,Title,MolecularFormula"
const pcbRequestTimeout = 30 * time.Second
const pcbCacheTTL = 24 * time.Hour
const pcbCIDBatchSize = 1000

// pcbFormulaCIDFetchCap bounds how many CIDs a single formula's fastformula
// result contributes before batched property-fetching
const pcbFormulaCIDFetchCap = 50

// pcbFallbackBudget is the belt-and-suspenders wall-clock backstop
const pcbMaxBatchedValues = 100000
const pcbMaxFormulaLookups = 50
const pcbFallbackBudget = 20 * time.Second

// TODO:
// test values - revisit once there is real traffic data against PubChem.
var (
	pcbMinGap        = 250 * time.Millisecond
	pcbThrottledGap  = 1500 * time.Millisecond
	pcbThrottlePause = 5 * time.Second
	pcbIdleReset     = 30 * time.Second
	pcbRetryDelays   = []time.Duration{2 * time.Second, 5 * time.Second}
)

// pcb429MaxRetries bounds how many times a throttled (429/503) call is
// retried before it counts as a failure toward the circuit breaker.
// pcbDownGiveUp is the number of consecutive non-throttle failures before the
// breaker trips and remaining calls in this request fail fast.
const pcb429MaxRetries = 5
const pcbDownGiveUp = 3

// The traced transport gives each round trip (retries included) its own child
// span under the pubchem_fallback span
var pcbHTTPClient = &http.Client{
	Timeout:   pcbRequestTimeout,
	Transport: telemetry.NewHTTPTransport(http.DefaultTransport, "PubChem"),
}

// pcbBreaker is shared across every PubChem call made while resolving
// fallback for a single /match request. Once tripped, remaining calls in
// that request fail fast rather than paying full retry latency
type pcbBreaker struct {
	downStreak int
	tripped    bool
	// Round trips and retries, tallied for the fallback summary log
	calls   int
	retries int
}

// pcbOutcome is the result of one gated, retried HTTP round trip
type pcbOutcome int
const (
	pcbFound pcbOutcome = iota
	pcbNotFound
	pcbFailed
)

// Request rate gate
var (
	pcbGateMu      sync.Mutex
	pcbNextAllowed time.Time
	pcbCurrentGap  = pcbMinGap
	pcbLastReqAt   time.Time
)

// pcbWaitTurn blocks until this call may proceed under the shared pacing gate.
func pcbWaitTurn(ctx context.Context) error {
	pcbGateMu.Lock()
	if time.Since(pcbLastReqAt) > pcbIdleReset {
		pcbCurrentGap = pcbMinGap
	}
	wait := time.Until(pcbNextAllowed)
	pcbGateMu.Unlock()
	return pcbSleep(ctx, wait)
}

// pcbSettlePacing sets the gap before the next call may start. ok is a clean
// 200/404 outcome (paces at pcbMinGap); throttled is a 429/503
func pcbSettlePacing(ok, throttled bool) {
	pcbGateMu.Lock()
	defer pcbGateMu.Unlock()
	now := time.Now()
	pcbLastReqAt = now
	switch {
	case throttled:
		pcbCurrentGap = pcbThrottledGap
		pcbNextAllowed = now.Add(pcbThrottlePause)
	case ok:
		pcbNextAllowed = now.Add(pcbMinGap)
	default:
		pcbNextAllowed = now.Add(pcbCurrentGap)
	}
}

func pcbSleep(ctx context.Context, d time.Duration) error {
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

// pcbRegisterFailure records a non-throttle failure against the breaker and
// reports whether the caller should still retry (false once tripped).
func pcbRegisterFailure(ctx context.Context, br *pcbBreaker) bool {
	br.downStreak++
	if br.downStreak >= pcbDownGiveUp {
		br.tripped = true
		pcbServiceUp.Store(false)
		pcbLogEvent(ctx, telemetry.PubChemEvent{Event: "breaker_tripped"},
			"appears down after %d consecutive failures; failing remaining calls for this request", br.downStreak)
		telemetry.AddSpanEvent(ctx, "pubchem.breaker_tripped", attribute.Int("consecutive_failures", br.downStreak))
		return false
	}
	return true
}

// pcbLogEvent writes a fallback event to stdout and OTel
func pcbLogEvent(ctx context.Context, ev telemetry.PubChemEvent, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// The path (endpoint and sometimes the query) stays out of the OTel body (OTel carries a trimmed 200-char max copy)
	if ev.Path != "" {
		log.Printf("PubChem fallback: %s [%s]", msg, ev.Path)
	} else {
		log.Printf("PubChem fallback: %s", msg)
	}
	telemetry.LogPubChemEvent(ctx, ev, msg)
}

// pcbRetryAfterFailure is the shared retry step for a non-throttle failure
func pcbRetryAfterFailure(ctx context.Context, br *pcbBreaker, attempt int, ev telemetry.PubChemEvent, what string) bool {
	ev.Attempt = attempt + 1
	if !pcbRegisterFailure(ctx, br) {
		ev.Event = "gave_up"
		pcbLogEvent(ctx, ev, "%s %s on attempt %d, giving up since PubChem appears down", ev.Endpoint, what, ev.Attempt)
		return false
	}
	if attempt >= len(pcbRetryDelays) {
		ev.Event = "gave_up"
		pcbLogEvent(ctx, ev, "%s %s on attempt %d, giving up after %d attempts", ev.Endpoint, what, ev.Attempt, ev.Attempt)
		return false
	}
	ev.Event = "retrying"
	ev.Delay = pcbRetryDelays[attempt]
	pcbLogEvent(ctx, ev, "%s %s on attempt %d, retrying in %s", ev.Endpoint, what, ev.Attempt, ev.Delay)
	br.retries++
	if pcbSleep(ctx, ev.Delay) != nil {
		pcbLogEvent(ctx, telemetry.PubChemEvent{Event: "budget_exhausted", Endpoint: ev.Endpoint, Path: ev.Path},
			"%s retry abandoned, the request budget ran out", ev.Endpoint)
		return false
	}
	return true
}

func pcbEndpointLabel(path string) string {
	switch {
	case strings.Contains(path, "/fastformula/"):
		return "fastformula"
	case strings.Contains(path, "/compound/inchikey"):
		return "inchikey"
	case strings.Contains(path, "/compound/cid/"):
		return "cid"
	}
	return "other"
}

// pcbExecute performs one gated, retried HTTP round trip and returns the raw
// response body on a terminal 200. A 404/400 is terminal-not-found (ok data,
// just nothing there), not an error
func pcbExecute(ctx context.Context, br *pcbBreaker, batchSize int, buildReq func() (*http.Request, error)) ([]byte, pcbOutcome) {
	if br.tripped {
		return nil, pcbFailed
	}

	for attempt := 0; ; attempt++ {
		if err := pcbWaitTurn(ctx); err != nil {
			pcbLogEvent(ctx, telemetry.PubChemEvent{Event: "budget_exhausted", Err: err},
				"gave up waiting for a pacing slot: %v", err)
			return nil, pcbFailed
		}

		req, err := buildReq()
		if err != nil {
			pcbLogEvent(ctx, telemetry.PubChemEvent{Event: "gave_up", Reason: "build_error", Err: err},
				"failed to build request: %v", err)
			return nil, pcbFailed
		}
		// Cut as to not bloat the logs
		endpoint, _, _ := strings.Cut(req.URL.Path, "/property/")
		label := pcbEndpointLabel(req.URL.Path)
		ev := telemetry.PubChemEvent{Endpoint: label, Path: endpoint}
		// only PubChem's own time is measured, not pacing/retry waits
		start := time.Now()
		record := func(result string) {
			telemetry.RecordPubChemFallbackRequest(ctx, label, result, time.Since(start), batchSize)
		}
		// A clean answer after a retry is logged
		logRecovered := func(statusCode int) {
			if attempt == 0 {
				return
			}
			rec := ev
			rec.Event = "recovered"
			rec.Attempt = attempt + 1
			rec.StatusCode = statusCode
			pcbLogEvent(ctx, rec, "%s answered %d on attempt %d", label, statusCode, attempt+1)
		}

		br.calls++
		resp, err := pcbHTTPClient.Do(req)
		if ctx.Err() != nil {
			// Usually the fallback budget expiring mid-flight
			ev.Event = "budget_exhausted"
			ev.Attempt = attempt + 1
			ev.Err = ctx.Err()
			pcbLogEvent(ctx, ev, "%s aborted after the request budget ran out: %v", label, ctx.Err())
			record("aborted")
			telemetry.AddSpanEvent(ctx, "pubchem.budget_exhausted", attribute.String("endpoint", endpoint))
			if resp != nil {
				resp.Body.Close()
			}
			return nil, pcbFailed
		}
		if err != nil {
			record("error")
			pcbSettlePacing(false, false)
			ev.Reason = "transport_error"
			ev.Err = err
			if pcbRetryAfterFailure(ctx, br, attempt, ev, fmt.Sprintf("request failed (%v)", err)) {
				continue
			}
			return nil, pcbFailed
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			record("error")
			pcbSettlePacing(false, false)
			ev.Reason = "read_error"
			ev.StatusCode = resp.StatusCode
			ev.Err = readErr
			what := fmt.Sprintf("returned %d but its body could not be read (%v)", resp.StatusCode, readErr)
			if pcbRetryAfterFailure(ctx, br, attempt, ev, what) {
				continue
			}
			return nil, pcbFailed
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			record("ok")
			br.downStreak = 0
			pcbSettlePacing(true, false)
			logRecovered(resp.StatusCode)
			return respBody, pcbFound

		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
			record("not_found")
			br.downStreak = 0
			pcbSettlePacing(true, false)
			logRecovered(resp.StatusCode)
			return nil, pcbNotFound

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			record("throttled")
			pcbSettlePacing(false, true)
			ev.Reason = "throttled"
			ev.StatusCode = resp.StatusCode
			ev.Attempt = attempt + 1
			if attempt >= pcb429MaxRetries {
				ev.Event = "gave_up"
				pcbLogEvent(ctx, ev, "%s still throttled (%d) after %d attempts, giving up",
					label, resp.StatusCode, attempt+1)
				pcbRegisterFailure(ctx, br)
				return nil, pcbFailed
			}
			ev.Event = "retrying"
			ev.Delay = pcbThrottlePause
			pcbLogEvent(ctx, ev, "%s throttled (%d) on attempt %d, retrying in %s",
				label, resp.StatusCode, attempt+1, pcbThrottlePause)
			// The pause shows in the trace as a gap, this labels the gap
			telemetry.AddSpanEvent(ctx, "pubchem.throttled",
				attribute.String("endpoint", endpoint),
				attribute.Int("status_code", resp.StatusCode),
				attribute.Int("attempt", attempt+1))
			br.retries++
			if pcbSleep(ctx, pcbThrottlePause) != nil {
				pcbLogEvent(ctx, telemetry.PubChemEvent{Event: "budget_exhausted", Endpoint: label, Path: endpoint},
					"%s retry abandoned, the request budget ran out", label)
				return nil, pcbFailed
			}
			continue

		default: // 500, 502, 504, etc.
			if resp.StatusCode == http.StatusInternalServerError && bytes.Contains(respBody, []byte("Search status indicates failure")) {
				record("not_found")
				br.downStreak = 0
				pcbSettlePacing(true, false)
				logRecovered(resp.StatusCode)
				return nil, pcbNotFound
			}
			record("error")
			pcbSettlePacing(false, false)
			ev.Reason = "http_error"
			ev.StatusCode = resp.StatusCode
			if pcbRetryAfterFailure(ctx, br, attempt, ev, fmt.Sprintf("returned %d", resp.StatusCode)) {
				continue
			}
			return nil, pcbFailed
		}
	}
}

// Cache
type pcbCacheEntry struct {
	compounds []*model.Compound
	expires   time.Time
}

var pcbCache sync.Map
func pcbCacheKey(kind, value string) string { return kind + "|" + value }

// pcbCacheLookup returns (nil, true) for a cached "not found" outcome, and
// (compounds, true) for a cached hit; ok is false on a cache miss
func pcbCacheLookup(kind, value string) ([]*model.Compound, bool) {
	if v, ok := pcbCache.Load(pcbCacheKey(kind, value)); ok {
		entry := v.(pcbCacheEntry)
		if time.Now().Before(entry.expires) {
			return entry.compounds, true
		}
		pcbCache.Delete(pcbCacheKey(kind, value))
	}
	return nil, false
}

func pcbCacheStore(kind, value string, compounds []*model.Compound) {
	pcbCache.Store(pcbCacheKey(kind, value), pcbCacheEntry{compounds: compounds, expires: time.Now().Add(pcbCacheTTL)})
}

// PubChem response parsing
type pcbProperty struct {
	CID                 int     `json:"CID"`
	InChIKey            string  `json:"InChIKey"`
	InChI               string  `json:"InChI"`
	SMILES              string  `json:"SMILES"`
	ExactMass           string  `json:"ExactMass"`
	LiteratureCount     float32 `json:"LiteratureCount"`
	PatentCount         float32 `json:"PatentCount"`
	AnnotationTypeCount float32 `json:"AnnotationTypeCount"`
	Title               string  `json:"Title"`
	MolecularFormula    string  `json:"MolecularFormula"`
}

type pcbPropertyResponse struct {
	PropertyTable struct {
		Properties []pcbProperty `json:"Properties"`
	} `json:"PropertyTable"`
}

func (p pcbProperty) toCompound() *model.Compound {
	mass, _ := strconv.ParseFloat(p.ExactMass, 64)
	return &model.Compound{
		Identifier:          strconv.Itoa(p.CID),
		InChIKey:            p.InChIKey,
		InChI:               p.InChI,
		Smiles:              p.SMILES,
		CompoundName:        p.Title,
		MolecularFormula:    p.MolecularFormula,
		ExactMass:           mass,
		LiteratureCount:     p.LiteratureCount,
		PatentCount:         p.PatentCount,
		AnnotationTypeCount: p.AnnotationTypeCount,
	}
}

func parsePropertyResponse(body []byte) ([]pcbProperty, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var pr pcbPropertyResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("decoding PubChem property response: %w", err)
	}
	return pr.PropertyTable.Properties, nil
}

type pcbCIDListResponse struct {
	IdentifierList struct {
		CID []int `json:"CID"`
	} `json:"IdentifierList"`
}

func parseCIDListResponse(body []byte) ([]int, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var lr pcbCIDListResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("decoding PubChem CID list response: %w", err)
	}
	return lr.IdentifierList.CID, nil
}

// --- Request builders ---

func pcbCIDPropertyRequest(ctx context.Context, cids []string) (*http.Request, error) {
	u := fmt.Sprintf("%s/compound/cid/%s/property/%s/JSON", pcbBaseURL, strings.Join(cids, ","), pcbPropertyList)
	return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
}

func pcbFormEncodedRequest(ctx context.Context, path, field, value string) (*http.Request, error) {
	u := fmt.Sprintf("%s/%s/property/%s/JSON", pcbBaseURL, path, pcbPropertyList)
	body := url.Values{field: {value}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

func pcbInChIKeyPropertyRequest(ctx context.Context, keys []string) (*http.Request, error) {
	return pcbFormEncodedRequest(ctx, "compound/inchikey", "inchikey", strings.Join(keys, ","))
}

// pcbFastFormulaRequest searches the formula with any charge suffix removed (O4P-3 becomes O4P)
func pcbFastFormulaRequest(ctx context.Context, formula string) (*http.Request, error) {
	if body, _, _, ok := splitChargeSuffix(formula); ok {
		formula = body
	}
	u := fmt.Sprintf("%s/compound/fastformula/%s/cids/JSON", pcbBaseURL, url.PathEscape(formula))
	return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
}

// fetch functions
func pcbChunk(values []string, size int) [][]string {
	var chunks [][]string
	for i := 0; i < len(values); i += size {
		end := i + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[i:end])
	}
	return chunks
}

// pcbFetchResult separates what a fetch call resolved from what it
// positively confirmed absent
type pcbFetchResult struct {
	found            map[string]*model.Compound
	confirmedMissing map[string]bool
}

func newPcbFetchResult() pcbFetchResult {
	return pcbFetchResult{found: map[string]*model.Compound{}, confirmedMissing: map[string]bool{}}
}

// Reports whether a batch would actually go out
func pcbCanSend(ctx context.Context, br *pcbBreaker) bool {
	return !br.tripped && ctx.Err() == nil
}

// Records what PubChem answered for each value of one
// sent batch: found, confirmed missing, or no answer
func pcbRecordBatchValues(ctx context.Context, endpoint string, part []string, res pcbFetchResult) {
	found, notFound := 0, 0
	for _, v := range part {
		switch {
		case res.found[v] != nil:
			found++
		case res.confirmedMissing[v]:
			notFound++
		}
	}
	telemetry.RecordPubChemFallbackValues(ctx, endpoint, found, notFound, len(part)-found-notFound)
}

// fetchCIDsBatched resolves a deduplicated list of PubChem CIDs to compounds
func fetchCIDsBatched(ctx context.Context, br *pcbBreaker, cids []string) pcbFetchResult {
	res := newPcbFetchResult()
	for _, part := range pcbChunk(cids, pcbCIDBatchSize) {
		sent := pcbCanSend(ctx, br)
		body, outcome := pcbExecute(ctx, br, len(part), func() (*http.Request, error) { return pcbCIDPropertyRequest(ctx, part) })
		switch outcome {
		case pcbFound:
			props, err := parsePropertyResponse(body)
			if err != nil {
				log.Printf("PubChem fallback: %v", err)
				break // malformed response - unknown, don't confirm missing
			}
			seen := make(map[string]bool, len(props))
			for _, p := range props {
				cid := strconv.Itoa(p.CID)
				res.found[cid] = p.toCompound()
				seen[cid] = true
			}
			// PubChem answered successfully but omitted these: a confirmed negative
			for _, c := range part {
				if !seen[c] {
					res.confirmedMissing[c] = true
				}
			}
		case pcbNotFound:
			for _, c := range part {
				res.confirmedMissing[c] = true
			}
		case pcbFailed:
			// unknown - leave unresolved so the next request retries fresh
		}
		if sent {
			pcbRecordBatchValues(ctx, "cid", part, res)
		}
	}
	return res
}

// fetchInChIKeysBatched resolves a deduplicated list of full InChIKeys to
// compounds in one request
func fetchInChIKeysBatched(ctx context.Context, br *pcbBreaker, keys []string) pcbFetchResult {
	res := newPcbFetchResult()
	for _, part := range pcbChunk(keys, pcbCIDBatchSize) {
		sent := pcbCanSend(ctx, br)
		body, outcome := pcbExecute(ctx, br, len(part), func() (*http.Request, error) { return pcbInChIKeyPropertyRequest(ctx, part) })
		switch outcome {
		case pcbFound:
			props, err := parsePropertyResponse(body)
			if err != nil {
				log.Printf("PubChem fallback: %v", err)
				break
			}
			seen := make(map[string]bool, len(props))
			for _, p := range props {
				if p.InChIKey == "" {
					continue
				}
				res.found[p.InChIKey] = p.toCompound()
				seen[p.InChIKey] = true
			}
			for _, k := range part {
				if !seen[k] {
					res.confirmedMissing[k] = true
				}
			}
		case pcbNotFound:
			for _, k := range part {
				res.confirmedMissing[k] = true
			}
		case pcbFailed:
			// unknown
		}
		if sent {
			pcbRecordBatchValues(ctx, "inchikey", part, res)
		}
	}
	return res
}

// RDKit is never asked to parse a string longer than this
const pcbMaxConvertibleLength = 4096

// deriveInChIKeys converts values to InChIKeys so they can ride the batched
// InChIKey lookup instead of costing one call each
func deriveInChIKeys(values []string, convert func(string) (string, error), precomputed map[string]string) (derived map[string]string, unparseable []string) {
	derived = make(map[string]string, len(values))
	for _, v := range values {
		key, reused := precomputed[v]
		if !reused {
			if len(v) > pcbMaxConvertibleLength {
				unparseable = append(unparseable, v)
				continue
			}
			var err error
			if key, err = convert(v); err != nil {
				key = ""
			}
		}
		if key == "" {
			unparseable = append(unparseable, v)
			continue
		}
		derived[v] = key
	}
	return derived, unparseable
}

// fetchFormulaCIDs resolves each unique formula to a bounded list of
// candidate CIDs via PubChem's fastformula search
func fetchFormulaCIDs(ctx context.Context, br *pcbBreaker, formulas []string) (byFormula map[string][]string, allCIDs []string, confirmedMissing map[string]bool) {
	byFormula = make(map[string][]string, len(formulas))
	confirmedMissing = make(map[string]bool)
	seen := make(map[string]bool)
	var sent []string
	for _, f := range formulas {
		if ctx.Err() != nil {
			break
		}
		if pcbCanSend(ctx, br) {
			sent = append(sent, f)
		}
		body, outcome := pcbExecute(ctx, br, 1, func() (*http.Request, error) { return pcbFastFormulaRequest(ctx, f) })
		if outcome == pcbNotFound {
			confirmedMissing[f] = true
			continue
		}
		if outcome != pcbFound {
			continue
		}
		cids, err := parseCIDListResponse(body)
		if err != nil {
			log.Printf("PubChem fallback: %v", err)
			continue
		}
		if len(cids) == 0 {
			confirmedMissing[f] = true
			continue
		}
		if len(cids) > pcbFormulaCIDFetchCap {
			cids = cids[:pcbFormulaCIDFetchCap]
		}
		strCIDs := make([]string, len(cids))
		for i, c := range cids {
			s := strconv.Itoa(c)
			strCIDs[i] = s
			if !seen[s] {
				seen[s] = true
				allCIDs = append(allCIDs, s)
			}
		}
		byFormula[f] = strCIDs
	}

	// A formula counts as found once fastformula returns candidates for it
	found, notFound := 0, 0
	for _, f := range sent {
		switch {
		case byFormula[f] != nil:
			found++
		case confirmedMissing[f]:
			notFound++
		}
	}
	telemetry.RecordPubChemFallbackValues(ctx, "fastformula", found, notFound, len(sent)-found-notFound)

	return byFormula, allCIDs, confirmedMissing
}

func pcbScore(c *model.Compound) float64 {
	return 0.7*float64(c.LiteratureCount) + 0.3*float64(c.PatentCount)
}

// pcbBuildFormulaResults resolves each formula's candidate CIDs against the
// already-fetched CID map, sorts by the cts-lite relevance score
func pcbBuildFormulaResults(byFormulaCIDs map[string][]string, cidResolved map[string]*model.Compound, topHitOnly bool) map[string][]*model.Compound {
	out := make(map[string][]*model.Compound, len(byFormulaCIDs))
	for formula, cids := range byFormulaCIDs {
		var candidates []*model.Compound
		for _, cid := range cids {
			if c, ok := cidResolved[cid]; ok {
				candidates = append(candidates, c)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			return pcbScore(candidates[i]) > pcbScore(candidates[j])
		})
		if topHitOnly && len(candidates) > 1 {
			candidates = candidates[:1]
		}
		out[formula] = candidates
	}
	return out
}

// Health probe
var pcbServiceUp atomic.Bool
func init() { pcbServiceUp.Store(true) }

const pcbHealthProbeInterval = 180 * time.Second
const pcbHealthProbeCID = "2244" // aspirin
const pcbHealthProbeTimeout = 15 * time.Second

// pcbHealthProbe is a var so tests can mock it
var pcbHealthProbe = func() bool {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/compound/cid/%s/property/Title/JSON", pcbBaseURL, pcbHealthProbeCID), nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: pcbHealthProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < http.StatusInternalServerError
}

// StartPubChemHealthCheck probes PubChem on startup and then periodically
func StartPubChemHealthCheck(ctx context.Context) {
	telemetry.RegisterPubChemServiceGauge(func() bool { return pcbServiceUp.Load() })
	go func() {
		check := func() { pcbServiceUp.Store(pcbHealthProbe()) }
		check()
		ticker := time.NewTicker(pcbHealthProbeInterval)
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

// Fallback resolution
// pcbMiss is one (result, kind, value) triple awaiting a PubChem attempt
type pcbMiss struct {
	result *model.SingleResult
	kind   string // "pubchem_id" | "inchi" | "inchikey" | "smiles" | "formula" | "smiles_or_formula"
	value  string
}

// collectPubChemMisses gathers every locally-missed result eligible for
// fallback. bad_inchi/bad_inchikey/unidentified are deliberately excluded.
// A local DB error ("Internal server error", set by matchPubChemID/matchInchi/etc. in
// api/match.go) is also excluded: that means the local lookup itself failed,
// not that the compound is absent
func collectPubChemMisses(results []*model.SingleResult) []pcbMiss {
	var misses []pcbMiss
	for _, r := range results {
		if r.MatchFound || r.ErrMsg == "Internal server error" {
			continue
		}
		switch r.QueryType {
		case "pubchem_id", "inchi", "inchikey", "smiles", "formula", "smiles_or_formula":
			misses = append(misses, pcbMiss{result: r, kind: r.QueryType, value: r.Query})
		}
	}
	return misses
}

// pcbResolutionContext bundles every resolved lookup map so resolveOne can
// answer any (kind, value) pair, including the smiles_or_formula composite
type pcbResolutionContext struct {
	cid      map[string]*model.Compound
	inchikey map[string]*model.Compound
	formula  map[string][]*model.Compound

	// SMILES and InChI values have no batch lookup of their own: RDKit
	// converts each one to an InChIKey which is then queried in batch
	smilesToKey map[string]string
	inchiToKey  map[string]string

	// Values PubChem answered for and said do not exist (400s, 404s)
	cidMissing      map[string]bool
	inchikeyMissing map[string]bool
	formulaMissing  map[string]bool
}

// pcbConfirmedMissing reports whether PubChem itself said (kind, value) has no
// match. It separates a real not_found from a value whose lookup was cut off by
// the breaker or budget
func pcbConfirmedMissing(kind, value string, rc pcbResolutionContext) bool {
	switch kind {
	case "pubchem_id":
		return rc.cidMissing[value]
	case "inchikey":
		return rc.inchikeyMissing[value]
	case "inchi":
		key, converted := rc.inchiToKey[value]
		return converted && rc.inchikeyMissing[key]
	case "smiles":
		key, converted := rc.smilesToKey[value]
		return converted && rc.inchikeyMissing[key]
	case "formula":
		return rc.formulaMissing[value]
	case "smiles_or_formula":
		if key, converted := rc.smilesToKey[value]; converted {
			return rc.inchikeyMissing[key]
		}
		return rc.formulaMissing[value]
	}
	return false
}

// resolveOne answers whether (kind, value) resolved, and if so the compounds,
// the MatchLevel string, and (for the converted and smiles/formula composite
// paths) the QueryType/ConvertedQuery overrides matching local match.go's conventions
func resolveOne(kind, value string, rc pcbResolutionContext) (compounds []*model.Compound, level, convType, convQuery string, ok bool) {
	switch kind {
	case "pubchem_id":
		if c, found := rc.cid[value]; found {
			return []*model.Compound{c}, "PubChem Live: Exact PubChem ID", "", "", true
		}
	case "inchi":
		if key, converted := rc.inchiToKey[value]; converted {
			if c, found := rc.inchikey[key]; found {
				// The key is reported as the converted query: it explains why an
				// inchi query comes back with an InChIKey match level
				return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "converted_inchi", key, true
			}
		}
	case "inchikey":
		if c, found := rc.inchikey[value]; found {
			return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "", "", true
		}
	case "smiles":
		if key, converted := rc.smilesToKey[value]; converted {
			if c, found := rc.inchikey[key]; found {
				return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "converted_smiles", key, true
			}
		}
	case "formula":
		if compounds, found := rc.formula[value]; found && len(compounds) > 0 {
			level := "PubChem Live: Formula"
			return compounds, level, "", "", true
		}
	case "smiles_or_formula":
		if cs, lvl, ct, cq, found := resolveOne("smiles", value, rc); found {
			return cs, lvl, ct, cq, true
		}
		if cs, lvl, _, _, found := resolveOne("formula", value, rc); found {
			return cs, lvl, "formula", "", true
		}
	}
	return nil, "", "", "", false
}

// pcbResolveKind checks the cache first before sending cache misses to PubChem
func pcbResolveKind(kind string, values []string, fetchFn func([]string) pcbFetchResult) pcbFetchResult {
	res := newPcbFetchResult()
	var toFetch []string
	for _, v := range values {
		if compounds, ok := pcbCacheLookup(kind, v); ok {
			if len(compounds) > 0 {
				res.found[v] = compounds[0]
			} else {
				res.confirmedMissing[v] = true
			}
			continue
		}
		toFetch = append(toFetch, v)
	}
	if len(toFetch) > 0 {
		fetched := fetchFn(toFetch)
		for v, c := range fetched.found {
			res.found[v] = c
			pcbCacheStore(kind, v, []*model.Compound{c})
		}
		for v := range fetched.confirmedMissing {
			res.confirmedMissing[v] = true
			pcbCacheStore(kind, v, nil)
		}
	}
	return res
}

// pcbFallbackCounts tallies the terminal outcome of each local miss
type pcbFallbackCounts struct {
	attempted, found, notFound, unparseable, failed, skipped int
}

// resolvePubChemFallback attempts a live PubChem lookup for every local miss
func resolvePubChemFallback(ctx context.Context, results []*model.SingleResult, topHitOnly bool) (counts pcbFallbackCounts) {
	misses := collectPubChemMisses(results)
	if len(misses) == 0 {
		return counts
	}

	start := time.Now()
	ctx, span := telemetry.StartSpan(ctx, "pubchem_fallback")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, pcbFallbackBudget)
	defer cancel()

	included := make(map[string]bool)
	batchedUsed, formulaUsed := 0, 0
	for _, m := range misses {
		key := m.kind + "|" + m.value
		if _, seen := included[key]; seen {
			continue
		}
		// Which budgets this value will draw on. smiles_or_formula draws on
		// both: it is queued into each bucket because either branch may answer
		usesBatch := m.kind != "formula"
		usesFormula := m.kind == "formula" || m.kind == "smiles_or_formula"

		hasRoom := (!usesBatch || batchedUsed < pcbMaxBatchedValues) &&
			(!usesFormula || formulaUsed < pcbMaxFormulaLookups)
		included[key] = hasRoom
		if !hasRoom {
			continue
		}
		if usesBatch {
			batchedUsed++
		}
		if usesFormula {
			formulaUsed++
		}
	}

	// Outcomes are tallied per query type, read off the result only once its
	// type is final, so they line up with what match_query_type_total records
	perType := map[string]*pcbFallbackCounts{}
	typeCounts := func(m pcbMiss) *pcbFallbackCounts {
		c := perType[m.result.QueryType]
		if c == nil {
			c = &pcbFallbackCounts{}
			perType[m.result.QueryType] = c
		}
		return c
	}

	var eligible []pcbMiss
	for _, m := range misses {
		if included[m.kind+"|"+m.value] {
			eligible = append(eligible, m)
			continue
		}
		typeCounts(m).skipped++
		m.result.ErrMsg = "No local match; PubChem live lookup limit reached"
	}

	// Group deduplicated values by kind.
	// Same try-smiles-first order. Formula branch is queued further down, and
	// only when the SMILES branch turns out not to be attemptable
	byKind := map[string][]string{}
	valueSeen := map[string]map[string]bool{}
	addToKindValueSet := func(kind, value string) {
		if valueSeen[kind] == nil {
			valueSeen[kind] = map[string]bool{}
		}
		if !valueSeen[kind][value] {
			valueSeen[kind][value] = true
			byKind[kind] = append(byKind[kind], value)
		}
	}
	ambiguous := map[string]bool{}
	for _, m := range eligible {
		addToKindValueSet(m.kind, m.value)
		if m.kind == "smiles_or_formula" {
			ambiguous[m.value] = true
			addToKindValueSet("smiles", m.value)
		}
	}

	br := &pcbBreaker{}

	// A value converts here when local matching never did,
	// which is the case for InChI and for a SMILES longer than
	// the local guard allows
	precomputedKeys := make(map[string]string)
	for _, m := range eligible {
		if m.result.DerivedInChIKey != "" {
			precomputedKeys[m.value] = m.result.DerivedInChIKey
		}
	}

	smilesToKey, badSmiles := deriveInChIKeys(byKind["smiles"], smilesToInChIKey, precomputedKeys)
	inchiToKey, badInChI := deriveInChIKeys(byKind["inchi"], inchiToInChIKey, precomputedKeys)
	for _, key := range smilesToKey {
		addToKindValueSet("inchikey", key)
	}
	for _, key := range inchiToKey {
		addToKindValueSet("inchikey", key)
	}

	// An ambiguous value only reaches fastformula when RDKit could not read it as a structure
	for _, v := range badSmiles {
		if ambiguous[v] {
			addToKindValueSet("formula", v)
		}
	}

	// Keyed by kind so a smiles_or_formula value, which often fails SMILES
	// parsing precisely because it is a formula, still reports whatever its
	// formula branch found rather than a parse complaint
	unparseable := make(map[string]bool, len(badSmiles)+len(badInChI))
	for _, v := range badSmiles {
		unparseable["smiles|"+v] = true
	}
	for _, v := range badInChI {
		unparseable["inchi|"+v] = true
	}

	// Batch calls run before the per-value formula calls
	cid := pcbResolveKind("pubchem_id", byKind["pubchem_id"], func(vs []string) pcbFetchResult {
		return fetchCIDsBatched(ctx, br, vs)
	})

	inchikey := pcbResolveKind("inchikey", byKind["inchikey"], func(vs []string) pcbFetchResult {
		return fetchInChIKeysBatched(ctx, br, vs)
	})

	// fastformula answers with CIDs rather than compounds, so formulas need a
	// second pass over the CID endpoint once their candidates are known
	byFormulaCIDs, allFormulaCIDs, formulaMissing := map[string][]string{}, []string{}, map[string]bool{}
	if len(byKind["formula"]) > 0 && ctx.Err() == nil {
		byFormulaCIDs, allFormulaCIDs, formulaMissing = fetchFormulaCIDs(ctx, br, byKind["formula"])
	}

	// Second CID batch after formula resolution so the real CID queries don't have to wait
	if len(allFormulaCIDs) > 0 {
		formulaCID := pcbResolveKind("pubchem_id", allFormulaCIDs, func(vs []string) pcbFetchResult {
			return fetchCIDsBatched(ctx, br, vs)
		})
		for c, compound := range formulaCID.found {
			cid.found[c] = compound
		}
		for c := range formulaCID.confirmedMissing {
			cid.confirmedMissing[c] = true
		}
	}

	formulaResolved := pcbBuildFormulaResults(byFormulaCIDs, cid.found, topHitOnly)

	// A formula whose candidates (CIDs) were not found by PubChem is as settled as
	// one fastformula denied outright
	for f, cids := range byFormulaCIDs {
		if len(formulaResolved[f]) > 0 {
			continue
		}
		allMissing := true
		for _, c := range cids {
			if !cid.confirmedMissing[c] {
				allMissing = false
				break
			}
		}
		if allMissing {
			formulaMissing[f] = true
		}
	}

	rc := pcbResolutionContext{
		cid:             cid.found,
		inchikey:        inchikey.found,
		formula:         formulaResolved,
		smilesToKey:     smilesToKey,
		inchiToKey:      inchiToKey,
		cidMissing:      cid.confirmedMissing,
		inchikeyMissing: inchikey.confirmedMissing,
		formulaMissing:  formulaMissing,
	}

	for _, m := range eligible {
		compounds, level, convType, convQuery, ok := resolveOne(m.kind, m.value, rc)
		if !ok {
			switch {
			case unparseable[m.kind+"|"+m.value]:
				// Never sent to PubChem, so neither attempted nor a "PubChem says no"
				typeCounts(m).unparseable++
				m.result.ErrMsg = "No local match; structure could not be read for PubChem lookup"
			case pcbConfirmedMissing(m.kind, m.value, rc):
				c := typeCounts(m)
				c.attempted++
				c.notFound++
				m.result.ErrMsg = "No compound found (including PubChem live lookup)"
			default:
				// No answer from PubChem: breaker, budget, or a call that gave up
				c := typeCounts(m)
				c.attempted++
				c.failed++
				m.result.ErrMsg = "No local match; PubChem live lookup unavailable"
			}
			continue
		}
		m.result.MatchFound = true
		m.result.ErrMsg = ""
		m.result.MatchLevel = level
		m.result.Matches = compounds
		// Set independently: a smiles or inchi hit becomes converted_smiles or
		// converted_inchi and reports the InChIKey it was converted to, while a
		// formula hit from smiles_or_formula resolves its type with no conversion to report
		if convType != "" {
			m.result.QueryType = convType
		}
		if convQuery != "" {
			m.result.ConvertedQuery = convQuery
		}
		// Tallied after the type override, so a resolved smiles_or_formula
		// counts as the smiles or formula it turned out to be
		c := typeCounts(m)
		c.attempted++
		c.found++
	}

	for queryType, c := range perType {
		telemetry.RecordPubChemFallbackOutcomes(ctx, queryType, c.found, c.notFound, c.unparseable, c.failed, c.skipped)
		counts.attempted += c.attempted
		counts.found += c.found
		counts.notFound += c.notFound
		counts.unparseable += c.unparseable
		counts.failed += c.failed
		counts.skipped += c.skipped
	}

	budgetExhausted := errors.Is(ctx.Err(), context.DeadlineExceeded)
	span.SetAttributes(
		attribute.Int("pubchem.misses", len(misses)),
		attribute.Int("pubchem.found", counts.found),
		attribute.Int("pubchem.not_found", counts.notFound),
		attribute.Int("pubchem.unparseable", counts.unparseable),
		attribute.Int("pubchem.failed", counts.failed),
		attribute.Int("pubchem.skipped", counts.skipped),
		attribute.Bool("pubchem.breaker_tripped", br.tripped),
		attribute.Bool("pubchem.budget_exhausted", budgetExhausted),
	)
	switch {
	case br.tripped:
		span.SetStatus(codes.Error, "PubChem circuit breaker tripped")
	case budgetExhausted:
		span.SetStatus(codes.Error, "PubChem fallback budget exhausted")
	}

	telemetry.LogPubChemFallbackSummary(ctx, telemetry.PubChemFallbackSummary{
		Misses:          len(misses),
		Attempted:       counts.attempted,
		Found:           counts.found,
		NotFound:        counts.notFound,
		Unparseable:     counts.unparseable,
		Failed:          counts.failed,
		Skipped:         counts.skipped,
		Calls:           br.calls,
		Retries:         br.retries,
		BreakerTripped:  br.tripped,
		BudgetExhausted: budgetExhausted,
		Duration:        time.Since(start),
	})

	return counts
}

