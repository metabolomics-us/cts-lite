package api

import (
	"bytes"
	"context"
	"encoding/json"
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

var pcbHTTPClient = &http.Client{Timeout: pcbRequestTimeout}

// pcbBreaker is shared across every PubChem call made while resolving
// fallback for a single /match request. Once tripped, remaining calls in
// that request fail fast rather than paying full retry latency
type pcbBreaker struct {
	downStreak int
	tripped    bool
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
func pcbRegisterFailure(br *pcbBreaker) bool {
	br.downStreak++
	if br.downStreak >= pcbDownGiveUp {
		br.tripped = true
		pcbServiceUp.Store(false)
		log.Printf("PubChem fallback: appears down after %d consecutive failures; failing remaining calls for this request", br.downStreak)
		return false
	}
	return true
}

// pcbExecute performs one gated, retried HTTP round trip and returns the raw
// response body on a terminal 200. A 404/400 is terminal-not-found (ok data,
// just nothing there), not an error.
func pcbExecute(ctx context.Context, br *pcbBreaker, buildReq func() (*http.Request, error)) ([]byte, pcbOutcome) {
	if br.tripped {
		return nil, pcbFailed
	}

	for attempt := 0; ; attempt++ {
		if err := pcbWaitTurn(ctx); err != nil {
			log.Printf("PubChem fallback: gave up waiting for a pacing slot: %v", err)
			return nil, pcbFailed
		}

		req, err := buildReq()
		if err != nil {
			log.Printf("PubChem fallback: failed to build request: %v", err)
			return nil, pcbFailed
		}
		// The property list runs ~100 characters and would swamp every log
		// line, while the path up to it already identifies the call
		endpoint, _, _ := strings.Cut(req.URL.Path, "/property/")

		resp, err := pcbHTTPClient.Do(req)
		if ctx.Err() != nil {
			// Usually the fallback budget expiring mid-flight
			log.Printf("PubChem fallback: %s aborted after the request budget ran out: %v", endpoint, ctx.Err())
			return nil, pcbFailed
		}
		if err != nil {
			log.Printf("PubChem fallback: request failed on attempt %d: %v", attempt+1, err)
			pcbSettlePacing(false, false)
			if !pcbRegisterFailure(br) || attempt >= len(pcbRetryDelays) {
				return nil, pcbFailed
			}
			if pcbSleep(ctx, pcbRetryDelays[attempt]) != nil {
				return nil, pcbFailed
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			log.Printf("PubChem fallback: %s returned %d but its body could not be read on attempt %d: %v",
				endpoint, resp.StatusCode, attempt+1, readErr)
			pcbSettlePacing(false, false)
			if !pcbRegisterFailure(br) || attempt >= len(pcbRetryDelays) {
				return nil, pcbFailed
			}
			if pcbSleep(ctx, pcbRetryDelays[attempt]) != nil {
				return nil, pcbFailed
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			br.downStreak = 0
			pcbSettlePacing(true, false)
			return respBody, pcbFound

		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
			br.downStreak = 0
			pcbSettlePacing(true, false)
			return nil, pcbNotFound

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			pcbSettlePacing(false, true)
			if attempt >= pcb429MaxRetries {
				log.Printf("PubChem fallback: %s still throttled (%d) after %d attempts, giving up",
					endpoint, resp.StatusCode, attempt+1)
				pcbRegisterFailure(br)
				return nil, pcbFailed
			}
			log.Printf("PubChem fallback: %s throttled (%d) on attempt %d, pausing %s",
				endpoint, resp.StatusCode, attempt+1, pcbThrottlePause)
			if pcbSleep(ctx, pcbThrottlePause) != nil {
				return nil, pcbFailed
			}
			continue

		default: // 500, 502, 504, etc.
			if resp.StatusCode == http.StatusInternalServerError && bytes.Contains(respBody, []byte("Search status indicates failure")) {
				br.downStreak = 0
				pcbSettlePacing(true, false)
				return nil, pcbNotFound
			}
			log.Printf("PubChem fallback: %s returned %d on attempt %d",
				endpoint, resp.StatusCode, attempt+1)
			pcbSettlePacing(false, false)
			if !pcbRegisterFailure(br) || attempt >= len(pcbRetryDelays) {
				return nil, pcbFailed
			}
			if pcbSleep(ctx, pcbRetryDelays[attempt]) != nil {
				return nil, pcbFailed
			}
			continue
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

func pcbFastFormulaRequest(ctx context.Context, formula string) (*http.Request, error) {
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

// fetchCIDsBatched resolves a deduplicated list of PubChem CIDs to compounds
func fetchCIDsBatched(ctx context.Context, br *pcbBreaker, cids []string) pcbFetchResult {
	res := newPcbFetchResult()
	for _, part := range pcbChunk(cids, pcbCIDBatchSize) {
		body, outcome := pcbExecute(ctx, br, func() (*http.Request, error) { return pcbCIDPropertyRequest(ctx, part) })
		switch outcome {
		case pcbFound:
			props, err := parsePropertyResponse(body)
			if err != nil {
				log.Printf("PubChem fallback: %v", err)
				continue // malformed response - unknown, don't confirm missing
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
	}
	return res
}

// fetchInChIKeysBatched resolves a deduplicated list of full InChIKeys to
// compounds in one request
func fetchInChIKeysBatched(ctx context.Context, br *pcbBreaker, keys []string) pcbFetchResult {
	res := newPcbFetchResult()
	for _, part := range pcbChunk(keys, pcbCIDBatchSize) {
		body, outcome := pcbExecute(ctx, br, func() (*http.Request, error) { return pcbInChIKeyPropertyRequest(ctx, part) })
		switch outcome {
		case pcbFound:
			props, err := parsePropertyResponse(body)
			if err != nil {
				log.Printf("PubChem fallback: %v", err)
				continue
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
	}
	return res
}

// deriveInChIKeys converts each value to an InChIKey locally via RDKit so it
// can ride the shared InChIKey batch instead of costing its own PubChem call
func deriveInChIKeys(values []string, convert func(string) (string, error)) (derived map[string]string, unparseable []string) {
	derived = make(map[string]string, len(values))
	for _, v := range values {
		key, err := convert(v)
		if err != nil || key == "" {
			unparseable = append(unparseable, v)
			continue
		}
		derived[v] = key
	}
	return derived, unparseable
}

// fetchFormulaCIDs resolves each unique formula to a bounded list of
// candidate CIDs via PubChem's fastformula search
func fetchFormulaCIDs(ctx context.Context, br *pcbBreaker, formulas []string) (byFormula map[string][]string, allCIDs []string) {
	byFormula = make(map[string][]string, len(formulas))
	seen := make(map[string]bool)
	for _, f := range formulas {
		if ctx.Err() != nil {
			break
		}
		body, outcome := pcbExecute(ctx, br, func() (*http.Request, error) { return pcbFastFormulaRequest(ctx, f) })
		if outcome != pcbFound {
			continue
		}
		cids, err := parseCIDListResponse(body)
		if err != nil {
			log.Printf("PubChem fallback: %v", err)
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
	return byFormula, allCIDs
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
}

// resolveOne answers whether (kind, value) resolved, and if so the compounds,
// the MatchLevel string, and (for the smiles/formula composite paths) the
// QueryType/ConvertedQuery overrides matching local match.go's conventions
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
				return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "", key, true
			}
		}
	case "inchikey":
		if c, found := rc.inchikey[value]; found {
			return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "", "", true
		}
	case "smiles":
		if key, converted := rc.smilesToKey[value]; converted {
			if c, found := rc.inchikey[key]; found {
				return []*model.Compound{c}, "PubChem Live: Exact InChIKey", "", key, true
			}
		}
	case "formula":
		if compounds, found := rc.formula[value]; found && len(compounds) > 0 {
			level := "PubChem Live: Formula (top hit)"
			if len(compounds) > 1 {
				level = fmt.Sprintf("PubChem Live: Formula (top %d)", len(compounds))
			}
			return compounds, level, "", "", true
		}
	case "smiles_or_formula":
		if cs, lvl, ct, cq, found := resolveOne("smiles", value, rc); found {
			if ct == "" {
				ct = "smiles"
			}
			return cs, lvl, ct, cq, true
		}
		if cs, lvl, _, _, found := resolveOne("formula", value, rc); found {
			return cs, lvl, "formula", "", true
		}
	}
	return nil, "", "", "", false
}

// pcbResolveKind checks the cache first, fetches only what's missing
func pcbResolveKind(kind string, values []string, fetchFn func([]string) pcbFetchResult) (resolved map[string]*model.Compound, fetchedCount int) {
	resolved = make(map[string]*model.Compound)
	var toFetch []string
	for _, v := range values {
		if compounds, ok := pcbCacheLookup(kind, v); ok {
			if len(compounds) > 0 {
				resolved[v] = compounds[0]
			}
			continue
		}
		toFetch = append(toFetch, v)
	}
	if len(toFetch) > 0 {
		fetched := fetchFn(toFetch)
		for v, c := range fetched.found {
			resolved[v] = c
			pcbCacheStore(kind, v, []*model.Compound{c})
		}
		for v := range fetched.confirmedMissing {
			pcbCacheStore(kind, v, nil)
		}
	}
	return resolved, len(toFetch)
}

// resolvePubChemFallback attempts a live PubChem lookup for every local miss
func resolvePubChemFallback(ctx context.Context, results []*model.SingleResult, topHitOnly bool) (attempted, found, notFound, failed, skipped int) {
	misses := collectPubChemMisses(results)
	if len(misses) == 0 {
		return 0, 0, 0, 0, 0
	}

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

	var eligible []pcbMiss
	for _, m := range misses {
		if included[m.kind+"|"+m.value] {
			eligible = append(eligible, m)
			continue
		}
		skipped++
		m.result.ErrMsg = "No local match; PubChem live lookup limit reached"
	}

	// Group deduplicated values by kind. smiles_or_formula values are queued
	// into both the smiles and formula buckets (mirroring matchSmilesOrFormula's
	// try-smiles-then-formula order) in addition to their own bucket, which
	// drives the final resolveOne("smiles_or_formula", ...) call
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
	for _, m := range eligible {
		addToKindValueSet(m.kind, m.value)
		if m.kind == "smiles_or_formula" {
			addToKindValueSet("smiles", m.value)
			addToKindValueSet("formula", m.value)
		}
	}

	br := &pcbBreaker{}

	// Formula CIDs are folded into the shared CID batch before it's fetched
	byFormulaCIDs, allFormulaCIDs := map[string][]string{}, []string{}
	formulaCalls := len(byKind["formula"])
	if formulaCalls > 0 && ctx.Err() == nil {
		byFormulaCIDs, allFormulaCIDs = fetchFormulaCIDs(ctx, br, byKind["formula"])
		for _, c := range allFormulaCIDs {
			addToKindValueSet("pubchem_id", c)
		}
	}

	cidResolved, cidFetched := pcbResolveKind("pubchem_id", byKind["pubchem_id"], func(vs []string) pcbFetchResult {
		return fetchCIDsBatched(ctx, br, vs)
	})

	// SMILES and InChI misses are converted locally to InChIKeys so they ride
	// the shared InChIKey batch instead of costing one call each. This has to
	// run before that batch is fetched, since the derived keys join its bucket
	smilesToKey, badSmiles := deriveInChIKeys(byKind["smiles"], smilesToInChIKey)
	inchiToKey, badInChI := deriveInChIKeys(byKind["inchi"], inchiToInChIKey)
	for _, key := range smilesToKey {
		addToKindValueSet("inchikey", key)
	}
	for _, key := range inchiToKey {
		addToKindValueSet("inchikey", key)
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

	inchikeyResolved, inchikeyFetched := pcbResolveKind("inchikey", byKind["inchikey"], func(vs []string) pcbFetchResult {
		return fetchInChIKeysBatched(ctx, br, vs)
	})

	formulaResolved := pcbBuildFormulaResults(byFormulaCIDs, cidResolved, topHitOnly)

	rc := pcbResolutionContext{
		cid:         cidResolved,
		inchikey:    inchikeyResolved,
		formula:     formulaResolved,
		smilesToKey: smilesToKey,
		inchiToKey:  inchiToKey,
	}

	for _, m := range eligible {
		attempted++
		compounds, level, convType, convQuery, ok := resolveOne(m.kind, m.value, rc)
		if !ok {
			switch {
			case unparseable[m.kind+"|"+m.value]:
				// Never sent to PubChem, so this is not a "PubChem says no"
				notFound++
				m.result.ErrMsg = "No local match; structure could not be read for PubChem lookup"
			case br.tripped || ctx.Err() != nil:
				failed++
				m.result.ErrMsg = "No local match; PubChem live lookup unavailable"
			default:
				notFound++
				m.result.ErrMsg = "No compound found (including PubChem live lookup)"
			}
			continue
		}
		found++
		m.result.MatchFound = true
		m.result.ErrMsg = ""
		m.result.MatchLevel = level
		m.result.Matches = compounds
		// Set independently: a smiles or inchi hit reports the InChIKey it was
		// converted to without its query type changing, while smiles_or_formula
		// resolves its type without necessarily having a conversion to report
		if convType != "" {
			m.result.QueryType = convType
		}
		if convQuery != "" {
			m.result.ConvertedQuery = convQuery
		}
	}

	batchedCalls := 0
	if cidFetched > 0 {
		batchedCalls += (cidFetched + pcbCIDBatchSize - 1) / pcbCIDBatchSize
	}
	if inchikeyFetched > 0 {
		batchedCalls += (inchikeyFetched + pcbCIDBatchSize - 1) / pcbCIDBatchSize
	}
	// fastformula is the only per-value call: it takes one formula
	// per request and cannot batch across distinct formulas. Everything else
	// reaches PubChem through a batched CID or InChIKey lookup
	individualCalls := formulaCalls

	telemetry.RecordPubChemFallbackOutcomes(ctx, found, notFound, failed, skipped)
	telemetry.RecordPubChemFallbackCallShape(ctx, batchedCalls, individualCalls)

	return attempted, found, notFound, failed, skipped
}

