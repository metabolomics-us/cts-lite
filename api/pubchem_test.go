package api

import (
	"bytes"
	"context"
	"ctslite/model"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetPcbPacing zeroes the pacing/retry delays so tests run fast, and
// resets shared gate state so tests don't interfere with each other
func resetPcbPacing(t *testing.T) {
	t.Helper()
	origMin, origThrottled, origPause := pcbMinGap, pcbThrottledGap, pcbThrottlePause
	origIdle, origRetry := pcbIdleReset, pcbRetryDelays
	origOwnGaps, origServiceGaps := pcbOwnLevelGaps, pcbServiceLevelGaps
	origMaxInFlight, origBudget := pcbMaxInFlight, pcbFallbackBudget
	pcbMinGap, pcbThrottledGap, pcbThrottlePause = 0, 0, 0
	pcbIdleReset = time.Hour
	pcbRetryDelays = []time.Duration{0, 0}
	pcbOwnLevelGaps = []time.Duration{0, 0, 0, 0}
	pcbServiceLevelGaps = []time.Duration{0, 0, 0, 0}

	pcbGateMu.Lock()
	pcbNextAllowed, pcbLastReqAt = time.Time{}, time.Time{}
	pcbCurrentGap = 0
	pcbInFlight, pcbPenalty, pcbStressed = 0, 0, false
	pcbGateMu.Unlock()

	t.Cleanup(func() {
		pcbMinGap, pcbThrottledGap, pcbThrottlePause = origMin, origThrottled, origPause
		pcbIdleReset, pcbRetryDelays = origIdle, origRetry
		pcbOwnLevelGaps, pcbServiceLevelGaps = origOwnGaps, origServiceGaps
		pcbMaxInFlight, pcbFallbackBudget = origMaxInFlight, origBudget
	})
}

// withMockPCBTransport swaps pcbHTTPClient for one backed by fn, restoring
// it after the test. Mirrors withMockTransport (api/classyfire_test.go),
// reusing the same mockRoundTripper type.
func withMockPCBTransport(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	orig := pcbHTTPClient
	pcbHTTPClient = &http.Client{Transport: mockRoundTripper{fn}}
	t.Cleanup(func() { pcbHTTPClient = orig })
}

func pcbResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// pcbClearCache removes cache entries so tests don't see stale results left
// by a previous test (pcbCache is a shared package-level sync.Map).
func pcbClearCache(t *testing.T, kind string, values ...string) {
	t.Helper()
	for _, v := range values {
		pcbCache.Delete(pcbCacheKey(kind, v))
	}
	t.Cleanup(func() {
		for _, v := range values {
			pcbCache.Delete(pcbCacheKey(kind, v))
		}
	})
}

func pcbPropertyJSON(props ...string) string {
	return fmt.Sprintf(`{"PropertyTable":{"Properties":[%s]}}`, strings.Join(props, ","))
}

// --- pcbChunk ---

func TestPcbChunkSplitsAtSize(t *testing.T) {
	got := pcbChunk([]string{"a", "b", "c", "d", "e"}, 2)
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if len(got) != len(want) {
		t.Fatalf("want %d chunks, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("chunk %d: want %v, got %v", i, want[i], got[i])
		}
	}
}

func TestPcbChunkEmptyInput(t *testing.T) {
	if got := pcbChunk(nil, 5); len(got) != 0 {
		t.Errorf("want 0 chunks for empty input, got %v", got)
	}
}

// --- pcbExecute retry/breaker behavior ---

func TestPcbExecuteRetriesThenSucceeds(t *testing.T) {
	resetPcbPacing(t)
	attempts := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return pcbResp(http.StatusInternalServerError, ""), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":180,"Title":"Acetone"}`)), nil
	})

	br := &pcbBreaker{}
	body, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFound {
		t.Fatalf("outcome: want pcbFound, got %v", outcome)
	}
	if attempts != 3 {
		t.Errorf("attempts: want 3, got %d", attempts)
	}
	if br.calls != 3 || br.retries != 2 {
		t.Errorf("calls/retries: want 3/2, got %d/%d", br.calls, br.retries)
	}
	props, err := parsePropertyResponse(body)
	if err != nil || len(props) != 1 || props[0].Title != "Acetone" {
		t.Errorf("unexpected parsed body: props=%+v err=%v", props, err)
	}
}

func TestPcbExecuteGivesUpAfterRetries(t *testing.T) {
	resetPcbPacing(t)
	attempts := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		attempts++
		return pcbResp(http.StatusInternalServerError, ""), nil
	})

	br := &pcbBreaker{}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFailed {
		t.Fatalf("outcome: want pcbFailed, got %v", outcome)
	}
	// pcbDownGiveUp consecutive failures trips the breaker
	if !br.tripped {
		t.Error("expected breaker to be tripped after repeated failures")
	}
}

func TestPcbExecuteTrippedBreakerFailsFast(t *testing.T) {
	resetPcbPacing(t)
	calls := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusInternalServerError, ""), nil
	})

	br := &pcbBreaker{tripped: true}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFailed {
		t.Fatalf("outcome: want pcbFailed, got %v", outcome)
	}
	if calls != 0 {
		t.Errorf("expected no network calls once breaker is tripped, got %d", calls)
	}
}

func TestPcbExecuteTerminalNotFoundDoesNotRetry(t *testing.T) {
	resetPcbPacing(t)
	calls := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusNotFound, ""), nil
	})

	br := &pcbBreaker{}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"999999999"})
	})
	if outcome != pcbNotFound {
		t.Fatalf("outcome: want pcbNotFound, got %v", outcome)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for a terminal 404, got %d", calls)
	}
	if br.tripped || br.downStreak != 0 {
		t.Error("a 404 should not count toward the circuit breaker")
	}
}

// A 414 (or another 4xx) cannot succeed on a resend, and PubChem did answer, so
// it is neither retried nor counted toward the breaker
func TestPcbExecuteClientErrorDoesNotRetry(t *testing.T) {
	resetPcbPacing(t)
	calls := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusRequestURITooLong, "Submitted URI too large!"), nil
	})

	br := &pcbBreaker{}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFailed {
		t.Fatalf("outcome: want pcbFailed, got %v", outcome)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for a 414, got %d", calls)
	}
	if br.tripped || br.downStreak != 0 {
		t.Error("a 414 should not count toward the circuit breaker")
	}
}

// A 200 whose body is empty or lacks its table says nothing about the values, so
// none may be confirmed missing (that would cache them as missing for 24h)
func TestPcbMalformedOKConfirmsNothingMissing(t *testing.T) {
	for _, body := range []string{"", "{}", `{"Fault":{"Code":"PUGREST.ServerError"}}`} {
		resetPcbPacing(t)
		withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
			return pcbResp(http.StatusOK, body), nil
		})

		res := fetchCIDsBatched(context.Background(), &pcbBreaker{}, []string{"180", "181"})
		if len(res.found) != 0 || len(res.confirmedMissing) != 0 {
			t.Errorf("CID batch, body %q: want nothing settled, got found %v missing %v", body, res.found, res.confirmedMissing)
		}

		pcbClearCache(t, "formula", "C9H9N9O9")
		byFormula, _, missing := fetchFormulaCIDs(context.Background(), &pcbBreaker{}, []string{"C9H9N9O9"})
		if len(byFormula) != 0 || missing["C9H9N9O9"] {
			t.Errorf("fastformula, body %q: want nothing settled, got candidates %v missing %v", body, byFormula, missing)
		}
		if _, cached := pcbFormulaCacheLookup("C9H9N9O9"); cached {
			t.Errorf("fastformula, body %q: want nothing cached", body)
		}
	}
}

// fastformula reports a malformed/unrecognized formula (bad element symbols,
// invalid syntax) as a 500 with this specific diagnostic body, unlike the
// property endpoints' plain 400/404
func TestPcbExecuteFastFormulaBadInputDoesNotRetry(t *testing.T) {
	resetPcbPacing(t)
	calls := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusInternalServerError, `{"Fault":{"Code":"PUGREST.ServerError","Message":"Search status indicates failure"}}`), nil
	})

	br := &pcbBreaker{}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbFastFormulaRequest(context.Background(), "C322cc")
	})
	if outcome != pcbNotFound {
		t.Fatalf("outcome: want pcbNotFound, got %v", outcome)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call, no retries, got %d", calls)
	}
	if br.tripped || br.downStreak != 0 {
		t.Error("a malformed-formula 500 should not count toward the circuit breaker")
	}
}

// Failures used to return silently: a call could burn the entire fallback
// budget and surface only as "1 failed" in the request summary, with nothing
// saying whether PubChem refused, hung, or was never reached. Each path should
// leave an operator something to work from
func TestPcbExecuteLogsWhyACallFailed(t *testing.T) {
	cases := []struct {
		name    string
		respond func(*http.Request) (*http.Response, error)
		want    string
	}{
		{
			name:    "network error",
			respond: func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") },
			want:    "connection refused",
		},
		{
			name:    "server error status",
			respond: func(*http.Request) (*http.Response, error) { return pcbResp(http.StatusBadGateway, ""), nil },
			want:    "502",
		},
		{
			name:    "throttled until give-up",
			respond: func(*http.Request) (*http.Response, error) { return pcbResp(http.StatusTooManyRequests, ""), nil },
			want:    "throttled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPcbPacing(t)
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			withMockPCBTransport(t, tc.respond)
			_, outcome := pcbExecute(context.Background(), &pcbBreaker{}, 1, func() (*http.Request, error) {
				return pcbCIDPropertyRequest(context.Background(), []string{"1"})
			})
			if outcome != pcbFailed {
				t.Fatalf("outcome: want pcbFailed, got %v", outcome)
			}
			if !strings.Contains(logged.String(), tc.want) {
				t.Errorf("log should explain the failure with %q, got:\n%s", tc.want, logged.String())
			}
		})
	}
}

// The case behind a 20s request that reported nothing but "1 failed": the
// budget expired mid-flight, which should not look like a PubChem fault
func TestPcbExecuteLogsBudgetExhaustion(t *testing.T) {
	resetPcbPacing(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ctx, cancel := context.WithCancel(context.Background())
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		cancel() // the budget runs out while this call is in flight
		return pcbResp(http.StatusOK, "{}"), nil
	})

	_, outcome := pcbExecute(ctx, &pcbBreaker{}, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"1"})
	})
	if outcome != pcbFailed {
		t.Fatalf("outcome: want pcbFailed, got %v", outcome)
	}
	if !strings.Contains(logged.String(), "budget") {
		t.Errorf("log should name the budget as the cause, got:\n%s", logged.String())
	}
}

func TestPcbExecuteThrottleRetriesThenSucceeds(t *testing.T) {
	resetPcbPacing(t)
	attempts := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts < 2 {
			return pcbResp(http.StatusTooManyRequests, ""), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":180}`)), nil
	})

	br := &pcbBreaker{}
	_, outcome := pcbExecute(context.Background(), br, 1, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFound {
		t.Fatalf("outcome: want pcbFound, got %v", outcome)
	}
	if br.downStreak != 0 {
		t.Error("a 429 that eventually succeeds should not leave the breaker's downStreak incremented")
	}
}

// --- Cache ---

func TestPcbCacheStoresPositiveAndNegative(t *testing.T) {
	pcbClearCache(t, "cid", "positive-case", "negative-case")

	pcbCacheStore("cid", "positive-case", []*model.Compound{{Identifier: "1"}})
	compounds, ok := pcbCacheLookup("cid", "positive-case")
	if !ok || len(compounds) != 1 {
		t.Fatalf("expected cached positive hit, got compounds=%v ok=%v", compounds, ok)
	}

	pcbCacheStore("cid", "negative-case", nil)
	compounds, ok = pcbCacheLookup("cid", "negative-case")
	if !ok || compounds != nil {
		t.Fatalf("expected cached negative hit (ok=true, nil compounds), got compounds=%v ok=%v", compounds, ok)
	}

	if _, ok := pcbCacheLookup("cid", "never-stored"); ok {
		t.Error("expected a genuine cache miss for a value never stored")
	}
}

func TestPcbResolveKindSkipsNetworkOnCacheHit(t *testing.T) {
	pcbClearCache(t, "cid", "cached-cid")
	pcbCacheStore("cid", "cached-cid", []*model.Compound{{Identifier: "cached-cid", CompoundName: "Cached"}})

	calls := 0
	res := pcbResolveKind("cid", []string{"cached-cid"}, func(vs []string) pcbFetchResult {
		calls++
		return newPcbFetchResult()
	})
	if calls != 0 {
		t.Errorf("expected 0 fetch calls for an all-cache-hit request, got %d", calls)
	}
	if res.found["cached-cid"] == nil || res.found["cached-cid"].CompoundName != "Cached" {
		t.Errorf("expected cached compound to be returned, got %+v", res.found["cached-cid"])
	}
}

// A cached negative is PubChem's earlier answer, so it must count as
// confirmed missing rather than as a value that never got an answer
func TestPcbResolveKindCachedNegativeIsConfirmedMissing(t *testing.T) {
	pcbClearCache(t, "cid", "cached-missing")
	pcbCacheStore("cid", "cached-missing", nil)

	res := pcbResolveKind("cid", []string{"cached-missing"}, func(vs []string) pcbFetchResult {
		t.Errorf("expected no fetch for a cached negative, got %v", vs)
		return newPcbFetchResult()
	})
	if !res.confirmedMissing["cached-missing"] {
		t.Error("expected a cached negative to be reported as confirmed missing")
	}
}

func TestPcbCacheSweepRemovesOnlyExpired(t *testing.T) {
	pcbClearCache(t, "cid", "sweep-expired", "sweep-fresh")
	pcbCache.Store(pcbCacheKey("cid", "sweep-expired"), pcbCacheEntry{expires: time.Now().Add(-time.Minute)})
	pcbCacheStore("cid", "sweep-fresh", nil)

	pcbCacheSweep(time.Now())
	if _, ok := pcbCache.Load(pcbCacheKey("cid", "sweep-expired")); ok {
		t.Error("expected the expired entry to be swept")
	}
	if _, ok := pcbCacheLookup("cid", "sweep-fresh"); !ok {
		t.Error("expected the unexpired entry to survive the sweep")
	}
}

// fastformula answers are cached too, so a repeated formula (hit or miss)
// never makes a second fastformula call
func TestFetchFormulaCIDsCachesAnswers(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "formula", "C031490", "C2H6O")
	calls := 0
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(r.URL.Path, "C031490") {
			return pcbResp(http.StatusNotFound, ""), nil
		}
		return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[702]}}`), nil
	})

	formulas := []string{"C031490", "C2H6O"}
	fetchFormulaCIDs(context.Background(), &pcbBreaker{}, formulas)
	byFormula, allCIDs, missing := fetchFormulaCIDs(context.Background(), &pcbBreaker{}, formulas)
	if calls != 2 {
		t.Errorf("expected 2 fastformula calls across both passes, got %d", calls)
	}
	if !missing["C031490"] {
		t.Error("expected the cached negative to be confirmed missing")
	}
	if !slices.Equal(byFormula["C2H6O"], []string{"702"}) || !slices.Equal(allCIDs, []string{"702"}) {
		t.Errorf("expected cached candidates [702], got byFormula=%v allCIDs=%v", byFormula, allCIDs)
	}
}

// --- Formula scoring/ranking ---

func TestPcbBuildFormulaResultsSortsByScoreAndAppliesTopHitOnly(t *testing.T) {
	byFormulaCIDs := map[string][]string{"C6H12O6": {"1", "2", "3"}}
	cidResolved := map[string]*model.Compound{
		"1": {Identifier: "1", LiteratureCount: 10, PatentCount: 0},  // score 7
		"2": {Identifier: "2", LiteratureCount: 100, PatentCount: 0}, // score 70 - highest
		"3": {Identifier: "3", LiteratureCount: 0, PatentCount: 0},   // score 0
	}

	full := pcbBuildFormulaResults(byFormulaCIDs, cidResolved, false)
	if len(full["C6H12O6"]) != 3 || full["C6H12O6"][0].Identifier != "2" {
		t.Fatalf("full ranking: want [2,1,3] by score, got %+v", full["C6H12O6"])
	}

	topOnly := pcbBuildFormulaResults(byFormulaCIDs, cidResolved, true)
	if len(topOnly["C6H12O6"]) != 1 || topOnly["C6H12O6"][0].Identifier != "2" {
		t.Fatalf("topHitOnly: want just [2], got %+v", topOnly["C6H12O6"])
	}
}

func TestPcbBuildFormulaResultsDropsUnresolvedCandidates(t *testing.T) {
	byFormulaCIDs := map[string][]string{"C6H12O6": {"1", "missing"}}
	cidResolved := map[string]*model.Compound{"1": {Identifier: "1"}}

	out := pcbBuildFormulaResults(byFormulaCIDs, cidResolved, false)
	if len(out["C6H12O6"]) != 1 || out["C6H12O6"][0].Identifier != "1" {
		t.Fatalf("expected only the resolved CID to survive, got %+v", out["C6H12O6"])
	}
}

// --- resolvePubChemFallback: end-to-end per query type ---

func missResult(queryType, query string) *model.SingleResult {
	return &model.SingleResult{Query: query, QueryType: queryType, MatchFound: false, ErrMsg: "No compound found"}
}

func TestResolvePubChemFallbackReturnsZeroWhenNoMisses(t *testing.T) {
	results := []*model.SingleResult{
		{Query: "180", QueryType: "pubchem_id", MatchFound: true, MatchLevel: "Exact PubChem ID"},
	}
	counts := resolvePubChemFallback(context.Background(), results, true)
	if counts != (pcbFallbackCounts{}) {
		t.Errorf("expected all-zero counts when nothing missed, got %+v", counts)
	}
}

func TestResolvePubChemFallbackNeverAttemptsMalformedQueryTypes(t *testing.T) {
	resetPcbPacing(t)
	calls := 0
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":1}`)), nil
	})

	results := []*model.SingleResult{
		missResult("bad_inchi", "inchi=garbage"),
		missResult("bad_inchikey", "NOTAREALKEY"),
		missResult("unidentified", "???"),
	}
	attempted := resolvePubChemFallback(context.Background(), results, true).attempted
	if attempted != 0 {
		t.Errorf("expected 0 attempts for malformed query types, got %d", attempted)
	}
	if calls != 0 {
		t.Errorf("expected 0 HTTP calls for malformed query types, got %d", calls)
	}
}

func TestResolvePubChemFallbackOnCIDMiss(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "180")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":180,"Title":"Acetone","MolecularFormula":"C3H6O"}`)), nil
	})

	results := []*model.SingleResult{missResult("pubchem_id", "180")}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	r := results[0]
	if !r.MatchFound || r.MatchLevel != "PubChem Live: Exact PubChem ID" {
		t.Errorf("unexpected result: MatchFound=%v MatchLevel=%q", r.MatchFound, r.MatchLevel)
	}
	if len(r.Matches) != 1 || r.Matches[0].CompoundName != "Acetone" {
		t.Errorf("unexpected matches: %+v", r.Matches)
	}
}

func TestResolvePubChemFallbackDedupsRepeatedCIDMisses(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "180")
	calls := 0
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":180,"Title":"Acetone"}`)), nil
	})

	results := []*model.SingleResult{
		missResult("pubchem_id", "180"),
		missResult("pubchem_id", "180"),
		missResult("pubchem_id", "180"),
	}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 3 {
		t.Fatalf("expected all 3 duplicate misses to resolve, got found=%d", found)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 HTTP call for 3 identical CID misses, got %d", calls)
	}
	for i, r := range results {
		if !r.MatchFound {
			t.Errorf("result[%d]: expected MatchFound, got false", i)
		}
	}
}

func TestResolvePubChemFallbackOnInChIKeyMiss(t *testing.T) {
	resetPcbPacing(t)
	key := "BSYNRYMUTXBXSQ-UHFFFAOYSA-N"
	pcbClearCache(t, "inchikey", key)
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(fmt.Sprintf(`{"CID":2244,"InChIKey":%q,"Title":"Aspirin"}`, key))), nil
	})

	results := []*model.SingleResult{missResult("inchikey", key)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	if results[0].MatchLevel != "PubChem Live: Exact InChIKey" {
		t.Errorf("unexpected MatchLevel: %q", results[0].MatchLevel)
	}
}

// RDKit converts InChI to an InChIKey which is what 
// actually gets queried, hence the InChIKey match level
func TestResolvePubChemFallbackOnInChIMiss(t *testing.T) {
	resetPcbPacing(t)
	inchi := "InChI=1S/C3H6O/c1-3(2)4/h1-2H3"
	key := "CSCPPACGZOOCGX-UHFFFAOYSA-N"
	pcbClearCache(t, "inchikey", key)

	mockInChIConverter(t, func(s string) (string, error) {
		if s != inchi {
			t.Errorf("unexpected InChI passed to converter: %q", s)
		}
		return key, nil
	})

	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "inchikey=") {
				t.Errorf("expected the derived key to go to the InChIKey endpoint, got body %q", body)
			}
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			fmt.Sprintf(`{"CID":180,"InChIKey":%q,"Title":"Acetone"}`, key))), nil
	})

	results := []*model.SingleResult{missResult("inchi", inchi)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 || results[0].MatchLevel != "PubChem Live: Exact InChIKey" {
		t.Fatalf("unexpected result: found=%d level=%q", found, results[0].MatchLevel)
	}
	// The derived key is reported so the InChIKey match level makes sense next
	// to the converted_inchi query type
	if results[0].ConvertedQuery != key {
		t.Errorf("expected ConvertedQuery %q, got %q", key, results[0].ConvertedQuery)
	}
	if results[0].QueryType != "converted_inchi" {
		t.Errorf("expected QueryType converted_inchi, got %q", results[0].QueryType)
	}
}

// Same two-hop path as InChI: SMILES is converted locally, the key is queried
func TestResolvePubChemFallbackOnSmilesMiss(t *testing.T) {
	resetPcbPacing(t)
	smiles := "CC(=O)C"
	key := "CSCPPACGZOOCGX-UHFFFAOYSA-N"
	pcbClearCache(t, "inchikey", key)

	mockSmilesConverter(t, func(s string) (string, error) {
		if s != smiles {
			t.Errorf("unexpected SMILES passed to converter: %q", s)
		}
		return key, nil
	})

	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			fmt.Sprintf(`{"CID":180,"InChIKey":%q,"Title":"Acetone"}`, key))), nil
	})

	results := []*model.SingleResult{missResult("smiles", smiles)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 || results[0].MatchLevel != "PubChem Live: Exact InChIKey" {
		t.Fatalf("unexpected result: found=%d level=%q", found, results[0].MatchLevel)
	}
	if results[0].ConvertedQuery != key {
		t.Errorf("expected ConvertedQuery %q, got %q", key, results[0].ConvertedQuery)
	}
	// Like local matchSmiles, a hit through the RDKit conversion is converted_smiles
	if results[0].QueryType != "converted_smiles" {
		t.Errorf("expected QueryType converted_smiles, got %q", results[0].QueryType)
	}
}

// The batching payoff: many distinct SMILES values cost one request, not one
// request each, because they all convert into the same InChIKey batch
func TestResolvePubChemFallbackBatchesSmilesIntoOneCall(t *testing.T) {
	resetPcbPacing(t)
	smiles := []string{"CC(=O)C", "CCO", "CCC", "CCCC"}
	keys := []string{
		"AAAAAAAAAAAAAA-AAAAAAAAAA-N",
		"BBBBBBBBBBBBBB-BBBBBBBBBB-N",
		"CCCCCCCCCCCCCC-CCCCCCCCCC-N",
		"DDDDDDDDDDDDDD-DDDDDDDDDD-N",
	}
	pcbClearCache(t, "inchikey", keys...)

	toKey := map[string]string{}
	for i, s := range smiles {
		toKey[s] = keys[i]
	}
	mockSmilesConverter(t, func(s string) (string, error) { return toKey[s], nil })

	calls := 0
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		props := make([]string, len(keys))
		for i, k := range keys {
			props[i] = fmt.Sprintf(`{"CID":%d,"InChIKey":%q,"Title":"C%d"}`, 100+i, k, i)
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(props...)), nil
	})

	var results []*model.SingleResult
	for _, s := range smiles {
		results = append(results, missResult("smiles", s))
	}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != len(smiles) {
		t.Fatalf("expected all %d to resolve, got %d", len(smiles), found)
	}
	if calls != 1 {
		t.Errorf("expected 4 distinct SMILES to batch into 1 call, got %d", calls)
	}
}

// A structure RDKit can't read never reaches PubChem
func TestResolvePubChemFallbackUnreadableSmilesSkipsPubChem(t *testing.T) {
	resetPcbPacing(t)
	smiles := "weird-smiles-rdkit-cant-parse"

	mockSmilesConverter(t, func(string) (string, error) { return "", nil })

	reachedPubChem := false
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		reachedPubChem = true
		return pcbResp(http.StatusNotFound, ""), nil
	})

	results := []*model.SingleResult{missResult("smiles", smiles)}
	counts := resolvePubChemFallback(context.Background(), results, true)
	if counts.unparseable != 1 || counts.notFound != 0 || counts.attempted != 0 {
		t.Fatalf("expected 1 unparseable, 0 not found, 0 attempted, got %+v", counts)
	}
	if reachedPubChem {
		t.Error("an unreadable structure should not reach PubChem at all")
	}
	if results[0].ErrMsg != "No compound found (structure could not be read)" {
		t.Errorf("unexpected ErrMsg: %q", results[0].ErrMsg)
	}
}

// fastformula ignores charge at best and fails or widens the search at worst,
// so the request should carry only the neutral formula
func TestPcbFastFormulaRequestStripsCharge(t *testing.T) {
	cases := map[string]string{
		"C7H16NO2+": "C7H16NO2",
		"Ca+2":      "Ca",
		"Cl-":       "Cl",
		"O4P-3":     "O4P",
		"C6H12O6":   "C6H12O6",
	}
	for formula, want := range cases {
		req, err := pcbFastFormulaRequest(context.Background(), formula)
		if err != nil {
			t.Fatalf("%s: %v", formula, err)
		}
		if wantPath := "/compound/fastformula/" + want + "/cids/JSON"; !strings.HasSuffix(req.URL.Path, wantPath) {
			t.Errorf("%s: path %q, want suffix %q", formula, req.URL.Path, wantPath)
		}
	}
}

func TestPcbCIDPropertyRequestPostsFullBatch(t *testing.T) {
	cids := make([]string, pcbCIDBatchSize)
	for i := range cids {
		cids[i] = strconv.Itoa(100_000_000 + i)
	}
	req, err := pcbCIDPropertyRequest(context.Background(), cids)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method: want POST, got %s", req.Method)
	}
	if n := len(req.URL.String()); n > 500 {
		t.Errorf("URL is %d chars, the CIDs should not be in it", n)
	}
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(req.PostForm.Get("cid"), ","); len(got) != len(cids) {
		t.Errorf("body: want %d CIDs, got %d", len(cids), len(got))
	}
	if label := pcbEndpointLabel(req.URL.Path); label != "cid" {
		t.Errorf("endpoint label: want cid, got %q", label)
	}
}

func TestResolvePubChemFallbackOnFormulaMiss(t *testing.T) {
	resetPcbPacing(t)
	formula := "C6H12O6"
	pcbClearCache(t, "formula", formula)
	pcbClearCache(t, "pubchem_id", "5793", "64689")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[5793,64689]}}`), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			`{"CID":5793,"Title":"D-Glucose","LiteratureCount":100,"PatentCount":10}`,
			`{"CID":64689,"Title":"Beta-D-Glucose","LiteratureCount":5,"PatentCount":1}`,
		)), nil
	})

	results := []*model.SingleResult{missResult("formula", formula)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	r := results[0]
	if r.MatchLevel != "PubChem Live: Formula" {
		t.Errorf("unexpected MatchLevel: %q", r.MatchLevel)
	}
	if len(r.Matches) != 1 || r.Matches[0].CompoundName != "D-Glucose" {
		t.Fatalf("expected the higher-scored compound (D-Glucose) as the top hit, got %+v", r.Matches)
	}
}

func TestResolvePubChemFallbackFormulaTopHitOnlyFalseReturnsAll(t *testing.T) {
	resetPcbPacing(t)
	formula := "C6H12O6"
	pcbClearCache(t, "formula", formula)
	pcbClearCache(t, "pubchem_id", "5793", "64689")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[5793,64689]}}`), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			`{"CID":5793,"Title":"D-Glucose","LiteratureCount":100,"PatentCount":10}`,
			`{"CID":64689,"Title":"Beta-D-Glucose","LiteratureCount":5,"PatentCount":1}`,
		)), nil
	})

	results := []*model.SingleResult{missResult("formula", formula)}
	found := resolvePubChemFallback(context.Background(), results, false).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	if len(results[0].Matches) != 2 {
		t.Fatalf("expected both formula candidates when topHitOnly=false, got %+v", results[0].Matches)
	}
}

// smiles_or_formula should try the SMILES lookup first (matching
// matchSmilesOrFormula's local order) and only use the formula result if the
// SMILES lookup misses
func TestResolvePubChemFallbackSmilesOrFormulaPrefersSmiles(t *testing.T) {
	resetPcbPacing(t)
	value := "C6H12O6" // structurally ambiguous: could be read as SMILES or formula
	key := "SMILESHIT00000-AAAAAAAAAA-N"
	pcbClearCache(t, "inchikey", key)

	mockSmilesConverter(t, func(string) (string, error) { return key, nil })

	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			fmt.Sprintf(`{"CID":42,"InChIKey":%q,"Title":"SmilesHit"}`, key))), nil
	})

	results := []*model.SingleResult{missResult("smiles_or_formula", value)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	r := results[0]
	if r.QueryType != "converted_smiles" {
		t.Errorf("expected QueryType to resolve to converted_smiles, got %q", r.QueryType)
	}
	// The composite sets both: the type it resolved to, and the key the SMILES
	// branch converted through
	if r.ConvertedQuery != key {
		t.Errorf("expected ConvertedQuery %q, got %q", key, r.ConvertedQuery)
	}
	if len(r.Matches) != 1 || r.Matches[0].CompoundName != "SmilesHit" {
		t.Errorf("expected the SMILES-resolved compound, got %+v", r.Matches)
	}
}

func TestResolvePubChemFallbackSmilesOrFormulaFallsBackToFormula(t *testing.T) {
	resetPcbPacing(t)
	value := "C6H12O6"
	pcbClearCache(t, "formula", value)
	pcbClearCache(t, "smiles", value)
	pcbClearCache(t, "pubchem_id", "5793")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "smiles=") {
				return pcbResp(http.StatusNotFound, ""), nil
			}
		}
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[5793]}}`), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":5793,"Title":"D-Glucose"}`)), nil
	})

	results := []*model.SingleResult{missResult("smiles_or_formula", value)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found via the formula fallback leg, got %d", found)
	}
	r := results[0]
	if r.QueryType != "formula" {
		t.Errorf("expected QueryType to resolve to formula, got %q", r.QueryType)
	}
	if len(r.Matches) != 1 || r.Matches[0].CompoundName != "D-Glucose" {
		t.Errorf("expected the formula-resolved compound, got %+v", r.Matches)
	}
}

// pcbRecordRequestKinds collects the kind of each PubChem request a test makes,
// so a test can assert which endpoints were reached and in what order
func pcbRecordRequestKinds(t *testing.T, respond func(*http.Request) (*http.Response, error)) *[]string {
	t.Helper()
	var seen []string
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "fastformula"):
			seen = append(seen, "fastformula")
		case strings.Contains(r.URL.Path, "/compound/cid/"):
			seen = append(seen, "cid")
		case strings.Contains(r.URL.Path, "/compound/inchikey/"):
			seen = append(seen, "inchikey")
		}
		return respond(r)
	})
	return &seen
}

// The two branches of smiles_or_formula run in sequence, not in parallel: a
// value RDKit can read is settled as SMILES and never reaches fastformula
func TestResolvePubChemFallbackSmilesOrFormulaSkipsFormulaWhenRDKitReads(t *testing.T) {
	resetPcbPacing(t)
	value := "CO" // both a formula (carbon monoxide) and a SMILES (methanol)
	key := "OKKJLVBELUTLKV-UHFFFAOYSA-N"
	pcbClearCache(t, "inchikey", key)
	pcbClearCache(t, "formula", value)
	pcbClearCache(t, "smiles", value)

	mockSmilesConverter(t, func(string) (string, error) { return key, nil })
	seen := pcbRecordRequestKinds(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(
			fmt.Sprintf(`{"CID":887,"InChIKey":%q,"Title":"Methanol"}`, key))), nil
	})

	results := []*model.SingleResult{missResult("smiles_or_formula", value)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	if slices.Contains(*seen, "fastformula") {
		t.Errorf("RDKit read the value, so fastformula should never be called; requests were %v", *seen)
	}
}

// The mirror case: RDKit cannot read it, so the formula branch is the only one
// left and fastformula is reached
func TestResolvePubChemFallbackSmilesOrFormulaUsesFormulaWhenRDKitCannot(t *testing.T) {
	resetPcbPacing(t)
	value := "B2Br2" // digits pair, so it stays ambiguous, but it is not a structure
	pcbClearCache(t, "formula", value)
	pcbClearCache(t, "smiles", value)
	pcbClearCache(t, "pubchem_id", "5359596")

	mockSmilesConverter(t, func(string) (string, error) { return "", nil })
	seen := pcbRecordRequestKinds(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[5359596]}}`), nil
		}
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":5359596,"Title":"Diboron dibromide"}`)), nil
	})

	results := []*model.SingleResult{missResult("smiles_or_formula", value)}
	found := resolvePubChemFallback(context.Background(), results, true).found
	if found != 1 {
		t.Fatalf("expected 1 found via the formula branch, got %d", found)
	}
	if !slices.Contains(*seen, "fastformula") {
		t.Errorf("RDKit could not read the value, so fastformula should be called; requests were %v", *seen)
	}
}

// A CID the request asked for directly is ready to send immediately, so its
// batch must not be held behind the serial per-value formula lookups
func TestResolvePubChemFallbackDirectCIDsDoNotWaitOnFormulaLookups(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "999999991", "5359596")
	pcbClearCache(t, "formula", "B2Br2")
	pcbClearCache(t, "smiles", "B2Br2")

	mockSmilesConverter(t, func(string) (string, error) { return "", nil })
	seen := pcbRecordRequestKinds(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[5359596]}}`), nil
		}
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	results := []*model.SingleResult{
		missResult("pubchem_id", "999999991"),
		missResult("smiles_or_formula", "B2Br2"),
	}
	resolvePubChemFallback(context.Background(), results, true)

	cidAt := slices.Index(*seen, "cid")
	formulaAt := slices.Index(*seen, "fastformula")
	if cidAt < 0 || formulaAt < 0 {
		t.Fatalf("expected both a cid batch and a fastformula call, got %v", *seen)
	}
	if cidAt > formulaAt {
		t.Errorf("the direct CID batch waited on the formula lookup; requests were %v", *seen)
	}
}

func TestResolvePubChemFallbackNotFoundSetsMessage(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "999999999")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	results := []*model.SingleResult{missResult("pubchem_id", "999999999")}
	notFound := resolvePubChemFallback(context.Background(), results, true).notFound
	if notFound != 1 {
		t.Fatalf("expected 1 not-found, got %d", notFound)
	}
	r := results[0]
	if r.MatchFound {
		t.Error("expected MatchFound to remain false")
	}
	if r.ErrMsg != "No compound found (including PubChem live lookup)" {
		t.Errorf("unexpected ErrMsg: %q", r.ErrMsg)
	}
}

func TestResolvePubChemFallbackServiceDownSetsUnavailableMessage(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "111", "222", "333")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusInternalServerError, ""), nil
	})

	results := []*model.SingleResult{
		missResult("pubchem_id", "111"),
	}
	failed := resolvePubChemFallback(context.Background(), results, true).failed
	if failed != 1 {
		t.Fatalf("expected 1 failed, got %d", failed)
	}
	if results[0].ErrMsg != "No local match; PubChem live lookup unavailable" {
		t.Errorf("unexpected ErrMsg: %q", results[0].ErrMsg)
	}
}

// A value PubChem already denied stays not_found when a later call trips the
// breaker, rather than being swept into failed with the unanswered values
func TestResolvePubChemFallbackConfirmedMissingSurvivesBreakerTrip(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "999999998")
	pcbClearCache(t, "formula", "C9H9")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			return pcbResp(http.StatusInternalServerError, ""), nil
		}
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	results := []*model.SingleResult{
		missResult("pubchem_id", "999999998"),
		missResult("formula", "C9H9"),
	}
	counts := resolvePubChemFallback(context.Background(), results, true)
	if counts.notFound != 1 || counts.failed != 1 {
		t.Fatalf("want 1 not found (the denied CID) and 1 failed (the formula), got %+v", counts)
	}
	if results[0].ErrMsg != "No compound found (including PubChem live lookup)" {
		t.Errorf("denied CID: unexpected ErrMsg %q", results[0].ErrMsg)
	}
	if results[1].ErrMsg != "No local match; PubChem live lookup unavailable" {
		t.Errorf("formula: unexpected ErrMsg %q", results[1].ErrMsg)
	}
}

// Same guarantee when the budget (here a cancelled context) ends the request
func TestResolvePubChemFallbackConfirmedMissingSurvivesBudgetExpiry(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "999999997")
	pcbClearCache(t, "formula", "C9H8")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "fastformula") {
			cancel() // the budget runs out during the formula lookup
			return pcbResp(http.StatusOK, `{"IdentifierList":{"CID":[1]}}`), nil
		}
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	results := []*model.SingleResult{
		missResult("pubchem_id", "999999997"),
		missResult("formula", "C9H8"),
	}
	counts := resolvePubChemFallback(ctx, results, true)
	if counts.notFound != 1 || counts.failed != 1 {
		t.Fatalf("want 1 not found (the denied CID) and 1 failed (the formula), got %+v", counts)
	}
}

// A call that gives up without tripping the breaker never got an answer, so it is failed
func TestResolvePubChemFallbackGiveUpWithoutBreakerIsFailed(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "444")
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		return pcbResp(http.StatusTooManyRequests, ""), nil
	})

	results := []*model.SingleResult{missResult("pubchem_id", "444")}
	counts := resolvePubChemFallback(context.Background(), results, true)
	if counts.failed != 1 || counts.notFound != 0 {
		t.Fatalf("want 1 failed, 0 not found, got %+v", counts)
	}
}

func TestPcbEndpointLabel(t *testing.T) {
	cases := map[string]string{
		"/rest/pug/compound/cid/180,181/property/Title/JSON": "cid",
		"/rest/pug/compound/inchikey/property/Title/JSON":    "inchikey",
		"/rest/pug/compound/fastformula/C6H12O6/cids/JSON":   "fastformula",
		"/rest/pug/somewhere/else":                           "other",
	}
	for path, want := range cases {
		if got := pcbEndpointLabel(path); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// Concurrent callers claim spaced start slots instead of all firing at once
func TestPcbWaitTurnSpacesConcurrentStarts(t *testing.T) {
	resetPcbPacing(t)
	pcbMinGap, pcbCurrentGap = 40*time.Millisecond, 40*time.Millisecond

	starts := make(chan time.Time, 3)
	for range 3 {
		go func() {
			if err := pcbWaitTurn(context.Background()); err != nil {
				t.Error(err)
			}
			starts <- time.Now()
		}()
	}
	var got []time.Time
	for range 3 {
		got = append(got, <-starts)
	}
	for range 3 {
		pcbSettlePacing(true, false, "")
	}
	slices.SortFunc(got, func(a, b time.Time) int { return a.Compare(b) })
	for i := 1; i < len(got); i++ {
		if d := got[i].Sub(got[i-1]); d < 35*time.Millisecond {
			t.Errorf("start %d came %s after the previous one, want about 40ms", i, d)
		}
	}
}

// A caller past the in-flight limit waits for a settle, and gives up with its context
func TestPcbWaitTurnLimitsInFlight(t *testing.T) {
	resetPcbPacing(t)
	for range pcbMaxInFlight {
		if err := pcbWaitTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := pcbWaitTurn(ctx); err == nil {
		t.Fatal("want a wait past the in-flight limit to end with the context")
	}
	pcbSettlePacing(true, false, "")
	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatalf("want a free spot after a settle, got %v", err)
	}
	for range pcbMaxInFlight {
		pcbSettlePacing(true, false, "")
	}
}

// Only our own budget or a Red/Black Service drops the gate to one in flight
func TestPcbParseThrottle(t *testing.T) {
	busyService := pcbParseThrottle("Request Count status: Green (5%), Request Time status: Green (0%), Service status: Yellow (60%)")
	if busyService.stressed() || busyService.gap() != pcbServiceLevelGaps[1] {
		t.Errorf("Service Yellow: want unstressed with a %s gap, got %+v gap %s", pcbServiceLevelGaps[1], busyService, busyService.gap())
	}
	ownYellow := pcbParseThrottle("Request Count status: Yellow (55%), Request Time status: Green (0%), Service status: Green (20%)")
	if !ownYellow.stressed() || ownYellow.gap() != pcbOwnLevelGaps[1] {
		t.Errorf("own Yellow: want stressed with a %s gap, got %+v gap %s", pcbOwnLevelGaps[1], ownYellow, ownYellow.gap())
	}
	if !pcbParseThrottle("Request Count status: Green (0%), Request Time status: Green (0%), Service status: Red (900%)").stressed() {
		t.Error("Service Red: want stressed")
	}
	if pcbParseThrottle("").reported() {
		t.Error("empty header: want nothing reported")
	}
	// Unknown colors and garbage are skipped rather than read as a level
	if r := pcbParseThrottle("Request Count status: Purple (5%), Service status: ??? (x%), <html>"); r.reported() {
		t.Errorf("malformed header: want nothing reported, got %+v", r)
	}
}

// pcbGateSnapshot reads the shared gate's in-flight count and current limit
func pcbGateSnapshot() (inFlight, limit int) {
	pcbGateMu.Lock()
	defer pcbGateMu.Unlock()
	return pcbInFlight, pcbGateLimit()
}

type pcbFailingBody struct{}

func (pcbFailingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset mid-body") }
func (pcbFailingBody) Close() error             { return nil }

// Every way out of pcbExecute gives its gate spot back. A lost spot is never
// recovered, and once all are lost every fallback call stalls
func TestPcbExecuteReleasesSpotOnEveryExit(t *testing.T) {
	cidReq := func() (*http.Request, error) { return pcbCIDPropertyRequest(context.Background(), []string{"180"}) }
	cases := []struct {
		name      string
		transport func(cancel context.CancelFunc) func(*http.Request) (*http.Response, error)
		buildReq  func() (*http.Request, error)
	}{
		{"found", func(context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":180}`)), nil }
		}, cidReq},
		{"build error", nil, func() (*http.Request, error) { return nil, errors.New("bad request") }},
		{"panic while building", nil, func() (*http.Request, error) { panic("boom") }},
		{"budget expires mid-flight", func(cancel context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { cancel(); return pcbResp(http.StatusOK, "{}"), nil }
		}, cidReq},
		{"transport error until the breaker trips", func(context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }
		}, cidReq},
		{"body read error", func(context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: pcbFailingBody{}, Header: make(http.Header)}, nil
			}
		}, cidReq},
		{"throttled until giving up", func(context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { return pcbResp(http.StatusServiceUnavailable, ""), nil }
		}, cidReq},
		{"server error until giving up", func(context.CancelFunc) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { return pcbResp(http.StatusBadGateway, ""), nil }
		}, cidReq},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetPcbPacing(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.transport != nil {
				withMockPCBTransport(t, c.transport(cancel))
			}
			func() {
				defer func() { recover() }()
				pcbExecute(ctx, &pcbBreaker{}, 1, c.buildReq)
			}()
			if inFlight, _ := pcbGateSnapshot(); inFlight != 0 {
				t.Errorf("want the gate spot released, %d still in flight", inFlight)
			}
		})
	}
}

// Our own Yellow drops the gate to one in flight, a reply without the header
// leaves that alone, and a Green reading lifts it again
func TestPcbSettlePacingStressFollowsHeader(t *testing.T) {
	resetPcbPacing(t)
	steps := []struct {
		header    string
		wantLimit int
	}{
		{"Request Count status: Yellow (55%), Request Time status: Green (0%), Service status: Green (10%)", 1},
		{"", 1},
		{"Request Count status: Green (5%), Request Time status: Green (0%), Service status: Yellow (60%)", pcbMaxInFlight},
		{"Request Count status: Green (5%), Request Time status: Green (0%), Service status: Black (900%)", 1},
		{"Request Count status: Green (5%), Request Time status: Green (0%), Service status: Green (10%)", pcbMaxInFlight},
	}
	for i, s := range steps {
		if err := pcbWaitTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		pcbSettlePacing(true, false, s.header)
		if _, limit := pcbGateSnapshot(); limit != s.wantLimit {
			t.Errorf("step %d (%q): want limit %d, got %d", i, s.header, s.wantLimit, limit)
		}
	}
}

// A throttle drops the gate to one in flight until the penalty has halved away
func TestPcbSettlePacingThrottlePenaltyDecays(t *testing.T) {
	resetPcbPacing(t)
	pcbMinGap, pcbThrottledGap = 10*time.Millisecond, 80*time.Millisecond

	settle := func(ok, throttled bool) int {
		if err := pcbWaitTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		pcbSettlePacing(ok, throttled, "")
		_, limit := pcbGateSnapshot()
		return limit
	}
	if limit := settle(false, true); limit != 1 {
		t.Fatalf("after a throttle: want limit 1, got %d", limit)
	}
	// 80ms halves to 40, 20, then 10, which is still at pcbMinGap
	for i := range 3 {
		if limit := settle(true, false); limit != 1 {
			t.Fatalf("clean answer %d: want limit 1 while the penalty remains, got %d", i+1, limit)
		}
	}
	if limit := settle(true, false); limit != pcbMaxInFlight {
		t.Errorf("once the penalty drops under pcbMinGap: want limit %d, got %d", pcbMaxInFlight, limit)
	}
}

// A caller waiting for a spot wakes as soon as one is settled
func TestPcbWaitTurnWakesWhenSpotFrees(t *testing.T) {
	resetPcbPacing(t)
	pcbMaxInFlight = 1
	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- pcbWaitTurn(context.Background()) }()

	select {
	case <-done:
		t.Fatal("second caller got past the gate while the only spot was taken")
	case <-time.After(30 * time.Millisecond):
	}
	pcbSettlePacing(true, false, "")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second caller never woke after the spot was freed")
	}
	pcbSettlePacing(true, false, "")
}

// A context ending while the caller waits for its start slot gives the spot back
func TestPcbWaitTurnCancelWhileWaitingForSlotReleasesSpot(t *testing.T) {
	resetPcbPacing(t)
	pcbGateMu.Lock()
	pcbNextAllowed = time.Now().Add(time.Second)
	pcbGateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := pcbWaitTurn(ctx); err == nil {
		t.Fatal("want the wait to end with the context")
	}
	if inFlight, _ := pcbGateSnapshot(); inFlight != 0 {
		t.Errorf("want the spot released, %d still in flight", inFlight)
	}
}

// A slot claimed before a throttle is not used, the caller queues again behind the pause
func TestPcbWaitTurnRequeuesSlotsClaimedBeforeThrottle(t *testing.T) {
	resetPcbPacing(t)
	pcbMinGap, pcbThrottlePause = 40*time.Millisecond, 150*time.Millisecond

	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan time.Time, 1)
	go func() {
		if err := pcbWaitTurn(context.Background()); err != nil {
			t.Error(err)
		}
		started <- time.Now()
	}()
	// Let the second caller claim its slot 40ms out, then throttle before it comes
	time.Sleep(10 * time.Millisecond)
	throttledAt := time.Now()
	pcbSettlePacing(false, true, "")

	if waited := (<-started).Sub(throttledAt); waited < 120*time.Millisecond {
		t.Errorf("want the second caller held until the 150ms pause ends, it started after %s", waited)
	}
	pcbSettlePacing(true, false, "")
}

// After an idle stretch with nothing in flight, stress and penalty from an old throttle are dropped
func TestPcbWaitTurnIdleResetClearsStress(t *testing.T) {
	resetPcbPacing(t)
	pcbIdleReset = 50 * time.Millisecond
	setStale := func(lastReq time.Time) {
		pcbGateMu.Lock()
		pcbStressed, pcbPenalty, pcbLastReqAt = true, time.Second, lastReq
		pcbGateMu.Unlock()
	}

	setStale(time.Now())
	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, limit := pcbGateSnapshot(); limit != 1 {
		t.Errorf("recent traffic: want stress kept (limit 1), got limit %d", limit)
	}
	pcbSettlePacing(false, false, "")

	setStale(time.Now().Add(-time.Second))
	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, limit := pcbGateSnapshot(); limit != pcbMaxInFlight {
		t.Errorf("after idling: want limit %d, got %d", pcbMaxInFlight, limit)
	}
	pcbSettlePacing(true, false, "")
}

// A healthy answer never pulls the next start earlier than slots already handed out
func TestPcbSettlePacingNeverMovesNextEarlier(t *testing.T) {
	resetPcbPacing(t)
	if err := pcbWaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	pcbGateMu.Lock()
	pcbNextAllowed = later
	pcbGateMu.Unlock()

	pcbSettlePacing(true, false, "")
	pcbGateMu.Lock()
	defer pcbGateMu.Unlock()
	if !pcbNextAllowed.Equal(later) {
		t.Errorf("want pcbNextAllowed left at %s, got %s", later.Format(time.StampMilli), pcbNextAllowed.Format(time.StampMilli))
	}
}

// --- Busy PubChem simulation ---

// pcbFakeBusyPubChem stands in for PubChem under load. It counts calls in a
// sliding window, reports that load in X-Throttling-Control the way PubChem
// does, and answers 503 ServerBusy at or over its limit. Every CID asked for
// is found otherwise
type pcbFakeBusyPubChem struct {
	window     time.Duration
	busyAt     int // calls in the window that read Yellow
	limit      int // calls in the window that get a 503
	latency    time.Duration
	alwaysBusy bool

	mu          sync.Mutex
	recent      []time.Time
	inFlight    int
	maxInFlight int
	busy        int // answers that read Yellow or worse
	throttled   int
}

func (f *pcbFakeBusyPubChem) roundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	now := time.Now()
	kept := f.recent[:0]
	for _, at := range f.recent {
		if now.Sub(at) < f.window {
			kept = append(kept, at)
		}
	}
	f.recent = append(kept, now)
	load := len(f.recent)
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	select {
	case <-time.After(f.latency):
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	level := "Green"
	switch {
	case f.alwaysBusy || load >= f.limit:
		level = "Black"
	case load >= f.busyAt:
		level = "Yellow"
	}
	header := fmt.Sprintf("Request Count status: %s (%d%%), Request Time status: Green (0%%), Service status: Green (10%%)",
		level, load*100/f.limit)
	f.mu.Lock()
	if level != "Green" {
		f.busy++
	}
	if level == "Black" {
		f.throttled++
	}
	f.mu.Unlock()

	var resp *http.Response
	if level == "Black" {
		resp = pcbResp(http.StatusServiceUnavailable, `{"Fault":{"Code":"PUGREST.ServerBusy"}}`)
	} else {
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		var props []string
		for _, c := range strings.Split(req.PostForm.Get("cid"), ",") {
			props = append(props, fmt.Sprintf(`{"CID":%s}`, c))
		}
		resp = pcbResp(http.StatusOK, pcbPropertyJSON(props...))
	}
	resp.Header.Set("X-Throttling-Control", header)
	return resp, nil
}

// pcbRunFallbacks runs one fallback per context at the same time, each for
// perRequest distinct CIDs, and returns each request's results
func pcbRunFallbacks(t *testing.T, ctxs []context.Context, perRequest, firstCID int) [][]*model.SingleResult {
	t.Helper()
	all := make([][]*model.SingleResult, len(ctxs))
	var wg sync.WaitGroup
	for i, ctx := range ctxs {
		var cids []string
		for j := range perRequest {
			cids = append(cids, strconv.Itoa(firstCID+i*perRequest+j))
		}
		pcbClearCache(t, "pubchem_id", cids...)
		for _, c := range cids {
			all[i] = append(all[i], missResult("pubchem_id", c))
		}
		wg.Add(1)
		go func(i int, ctx context.Context) {
			defer wg.Done()
			resolvePubChemFallback(ctx, all[i], true)
		}(i, ctx)
	}
	wg.Wait()
	return all
}

func pcbBackgroundContexts(n int) []context.Context {
	ctxs := make([]context.Context, n)
	for i := range ctxs {
		ctxs[i] = context.Background()
	}
	return ctxs
}

// PubChem turns busy under a burst of concurrent requests. The gate backs off,
// never exceeds its in-flight limit, and with enough budget every value is answered
func TestResolvePubChemFallbackRidesOutBusyPubChem(t *testing.T) {
	resetPcbPacing(t)
	pcbMinGap, pcbThrottledGap, pcbThrottlePause = 2*time.Millisecond, 20*time.Millisecond, 30*time.Millisecond
	pcbOwnLevelGaps = []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	pcbFallbackBudget = 10 * time.Second
	fake := &pcbFakeBusyPubChem{window: 100 * time.Millisecond, busyAt: 4, limit: 8, latency: 5 * time.Millisecond}
	withMockPCBTransport(t, fake.roundTrip)

	for i, results := range pcbRunFallbacks(t, pcbBackgroundContexts(12), 3, 910000) {
		for _, r := range results {
			if !r.MatchFound {
				t.Errorf("request %d, CID %s: want found once PubChem recovers, got %q", i, r.Query, r.ErrMsg)
			}
		}
	}
	if fake.busy == 0 {
		t.Error("the fake never reported load, so this test did not exercise a busy PubChem")
	}
	if fake.maxInFlight > pcbMaxInFlight {
		t.Errorf("want at most %d calls in flight, PubChem saw %d", pcbMaxInFlight, fake.maxInFlight)
	}
	if inFlight, _ := pcbGateSnapshot(); inFlight != 0 {
		t.Errorf("want the gate drained, %d still in flight", inFlight)
	}
}

// PubChem stays saturated past the budget. Unanswered values come back
// unavailable, and the request itself still returns on time
func TestResolvePubChemFallbackBusyPastBudgetMarksUnavailable(t *testing.T) {
	resetPcbPacing(t)
	pcbThrottlePause, pcbFallbackBudget = 30*time.Millisecond, 150*time.Millisecond
	fake := &pcbFakeBusyPubChem{window: time.Second, limit: 1, latency: time.Millisecond, alwaysBusy: true}
	withMockPCBTransport(t, fake.roundTrip)

	start := time.Now()
	all := pcbRunFallbacks(t, pcbBackgroundContexts(4), 2, 920000)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("want every request back shortly after the 150ms budget, took %s", elapsed)
	}
	for i, results := range all {
		for _, r := range results {
			if r.MatchFound || r.ErrMsg != "No local match; PubChem live lookup unavailable" {
				t.Errorf("request %d, CID %s: want unavailable, got found=%v %q", i, r.Query, r.MatchFound, r.ErrMsg)
			}
		}
	}
	if inFlight, _ := pcbGateSnapshot(); inFlight != 0 {
		t.Errorf("want the gate drained, %d still in flight", inFlight)
	}
}

// A request whose client goes away while queued at the gate stops waiting and
// frees nothing it does not hold, while the request ahead of it still completes
func TestResolvePubChemFallbackCancelledWhileQueuedLeavesOthersRunning(t *testing.T) {
	resetPcbPacing(t)
	pcbMaxInFlight = 1
	fake := &pcbFakeBusyPubChem{window: time.Second, busyAt: 1000, limit: 1000, latency: 150 * time.Millisecond}
	withMockPCBTransport(t, fake.roundTrip)

	first := []*model.SingleResult{missResult("pubchem_id", "930001")}
	queued := []*model.SingleResult{missResult("pubchem_id", "930002")}
	pcbClearCache(t, "pubchem_id", "930001", "930002")

	firstDone := make(chan struct{})
	go func() {
		resolvePubChemFallback(context.Background(), first, true)
		close(firstDone)
	}()
	time.Sleep(10 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	resolvePubChemFallback(ctx, queued, true)
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Errorf("want the queued request to give up with its client, it waited %s", waited)
	}
	if queued[0].ErrMsg != "No local match; PubChem live lookup unavailable" {
		t.Errorf("queued request: want unavailable, got %q", queued[0].ErrMsg)
	}

	<-firstDone
	if !first[0].MatchFound {
		t.Errorf("first request: want found, got %q", first[0].ErrMsg)
	}
	if inFlight, _ := pcbGateSnapshot(); inFlight != 0 {
		t.Errorf("want the gate drained, %d still in flight", inFlight)
	}
}

// Batched values are rationed by what they cost, not by how many there are:
// 250 InChIKeys fit in a single request, so none of them should be turned away
func TestResolvePubChemFallbackDoesNotRationBatchedValues(t *testing.T) {
	resetPcbPacing(t)
	var results []*model.SingleResult
	for i := 0; i < 250; i++ {
		key := fmt.Sprintf("AAAAAAAAAAAA%02d-AAAAAAAAAA-N", i)
		pcbClearCache(t, "inchikey", key)
		results = append(results, missResult("inchikey", key))
	}

	calls := 0
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	counts := resolvePubChemFallback(context.Background(), results, true)
	attempted, skipped := counts.attempted, counts.skipped
	if attempted != 250 {
		t.Errorf("attempted: want all 250, got %d", attempted)
	}
	if skipped != 0 {
		t.Errorf("skipped: want 0, got %d", skipped)
	}
	if calls != 1 {
		t.Errorf("250 keys should be one chunked request, got %d calls", calls)
	}
}

// Formulas are rationed tightly, since each one is its own fastformula call
func TestResolvePubChemFallbackFormulaCapSkipsExcess(t *testing.T) {
	resetPcbPacing(t)
	var results []*model.SingleResult
	for i := 0; i < pcbMaxFormulaLookups+1; i++ {
		formula := fmt.Sprintf("C%dH%d", i+1, i+2)
		pcbClearCache(t, "formula", formula)
		results = append(results, missResult("formula", formula))
	}
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusNotFound, ""), nil
	})

	counts := resolvePubChemFallback(context.Background(), results, true)
	attempted, skipped := counts.attempted, counts.skipped
	if attempted != pcbMaxFormulaLookups {
		t.Errorf("attempted: want %d, got %d", pcbMaxFormulaLookups, attempted)
	}
	if skipped != 1 {
		t.Errorf("skipped: want 1, got %d", skipped)
	}
	last := results[len(results)-1]
	if last.ErrMsg != "No local match; PubChem live lookup limit reached" {
		t.Errorf("unexpected ErrMsg on the capped-out result: %q", last.ErrMsg)
	}
}

// --- Health probe / status ---

func TestStartPubChemHealthCheckUpdatesFlag(t *testing.T) {
	orig := pcbHealthProbe
	t.Cleanup(func() { pcbHealthProbe = orig })

	// StartPubChemHealthCheck's first probe runs inside its own goroutine,
	// asynchronously with respect to the caller - a channel rendezvous is
	// needed before reading pcbServiceUp or reassigning pcbHealthProbe again,
	// otherwise the test races with that goroutine's read of pcbHealthProbe
	probed := make(chan struct{}, 1)
	pcbHealthProbe = func() bool {
		select {
		case probed <- struct{}{}:
		default:
		}
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	StartPubChemHealthCheck(ctx)
	<-probed // wait for the goroutine's first (and, within this test, only) probe

	if pcbServiceUp.Load() {
		t.Error("expected pcbServiceUp to reflect the probe's false result")
	}

	// The background goroutine only re-reads pcbHealthProbe on its next
	// ticker interval (180s) or after ctx is cancelled (a plain return, no
	// read) - safe to call it directly and reassign it now.
	pcbHealthProbe = func() bool { return true }
	pcbServiceUp.Store(pcbHealthProbe())
	if !pcbServiceUp.Load() {
		t.Error("expected pcbServiceUp to update to true")
	}

	cancel()
}
