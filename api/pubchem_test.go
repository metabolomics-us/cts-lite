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
	"strings"
	"testing"
	"time"
)

// resetPcbPacing zeroes the pacing/retry delays so tests run fast, and
// resets shared gate state so tests don't interfere with each other
func resetPcbPacing(t *testing.T) {
	t.Helper()
	origMin, origThrottled, origPause := pcbMinGap, pcbThrottledGap, pcbThrottlePause
	origIdle, origRetry := pcbIdleReset, pcbRetryDelays
	pcbMinGap, pcbThrottledGap, pcbThrottlePause, pcbIdleReset = 0, 0, 0, 0
	pcbRetryDelays = []time.Duration{0, 0}

	pcbGateMu.Lock()
	pcbNextAllowed, pcbLastReqAt = time.Time{}, time.Time{}
	pcbCurrentGap = 0
	pcbGateMu.Unlock()

	t.Cleanup(func() {
		pcbMinGap, pcbThrottledGap, pcbThrottlePause = origMin, origThrottled, origPause
		pcbIdleReset, pcbRetryDelays = origIdle, origRetry
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
	body, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
		return pcbCIDPropertyRequest(context.Background(), []string{"180"})
	})
	if outcome != pcbFound {
		t.Fatalf("outcome: want pcbFound, got %v", outcome)
	}
	if attempts != 3 {
		t.Errorf("attempts: want 3, got %d", attempts)
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
	_, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
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
	_, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
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
	_, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
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
	_, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
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
			_, outcome := pcbExecute(context.Background(), &pcbBreaker{}, func() (*http.Request, error) {
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

	_, outcome := pcbExecute(ctx, &pcbBreaker{}, func() (*http.Request, error) {
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
	_, outcome := pcbExecute(context.Background(), br, func() (*http.Request, error) {
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
	resolved, fetchedCount := pcbResolveKind("cid", []string{"cached-cid"}, func(vs []string) pcbFetchResult {
		calls++
		return newPcbFetchResult()
	})
	if calls != 0 {
		t.Errorf("expected 0 fetch calls for an all-cache-hit request, got %d", calls)
	}
	if fetchedCount != 0 {
		t.Errorf("fetchedCount: want 0, got %d", fetchedCount)
	}
	if resolved["cached-cid"] == nil || resolved["cached-cid"].CompoundName != "Cached" {
		t.Errorf("expected cached compound to be returned, got %+v", resolved["cached-cid"])
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
	attempted, found, notFound, failed, skipped := resolvePubChemFallback(context.Background(), results, true)
	if attempted != 0 || found != 0 || notFound != 0 || failed != 0 || skipped != 0 {
		t.Errorf("expected all-zero counts when nothing missed, got attempted=%d found=%d notFound=%d failed=%d skipped=%d",
			attempted, found, notFound, failed, skipped)
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
	attempted, _, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != 1 || results[0].MatchLevel != "PubChem Live: Exact InChIKey" {
		t.Fatalf("unexpected result: found=%d level=%q", found, results[0].MatchLevel)
	}
	// The derived key is reported so the InChIKey match level makes sense next
	// to an inchi query type, which itself stays untouched
	if results[0].ConvertedQuery != key {
		t.Errorf("expected ConvertedQuery %q, got %q", key, results[0].ConvertedQuery)
	}
	if results[0].QueryType != "inchi" {
		t.Errorf("expected QueryType to stay inchi, got %q", results[0].QueryType)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != 1 || results[0].MatchLevel != "PubChem Live: Exact InChIKey" {
		t.Fatalf("unexpected result: found=%d level=%q", found, results[0].MatchLevel)
	}
	if results[0].ConvertedQuery != key {
		t.Errorf("expected ConvertedQuery %q, got %q", key, results[0].ConvertedQuery)
	}
	// Unlike local matchSmiles, the fallback leaves the query type alone: only
	// ConvertedQuery and the match level record that a conversion happened
	if results[0].QueryType != "smiles" {
		t.Errorf("expected QueryType to stay smiles, got %q", results[0].QueryType)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != len(smiles) {
		t.Fatalf("expected all %d to resolve, got %d", len(smiles), found)
	}
	if calls != 1 {
		t.Errorf("expected 4 distinct SMILES to batch into 1 call, got %d", calls)
	}
}

// A structure RDKit can't read never reaches PubChem: there is no InChIKey to
// put in the batch, and PubChem's own SMILES endpoint can't batch, so there is
// nothing to gain by asking it separately. The message says so rather than
// implying PubChem was asked and answered no
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
	_, found, notFound, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != 0 || notFound != 1 {
		t.Fatalf("expected 0 found, 1 not found, got found=%d notFound=%d", found, notFound)
	}
	if reachedPubChem {
		t.Error("an unreadable structure should not reach PubChem at all")
	}
	if results[0].ErrMsg != "No local match; structure could not be read for PubChem lookup" {
		t.Errorf("unexpected ErrMsg: %q", results[0].ErrMsg)
	}
}

func TestResolvePubChemFallbackOnFormulaMiss(t *testing.T) {
	resetPcbPacing(t)
	formula := "C6H12O6"
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	r := results[0]
	if r.MatchLevel != "PubChem Live: Formula (top hit)" {
		t.Errorf("unexpected MatchLevel: %q", r.MatchLevel)
	}
	if len(r.Matches) != 1 || r.Matches[0].CompoundName != "D-Glucose" {
		t.Fatalf("expected the higher-scored compound (D-Glucose) as the top hit, got %+v", r.Matches)
	}
}

func TestResolvePubChemFallbackFormulaTopHitOnlyFalseReturnsAll(t *testing.T) {
	resetPcbPacing(t)
	formula := "C6H12O6"
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, false)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
	if found != 1 {
		t.Fatalf("expected 1 found, got %d", found)
	}
	r := results[0]
	if r.QueryType != "smiles" {
		t.Errorf("expected QueryType to resolve to smiles, got %q", r.QueryType)
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
	_, found, _, _, _ := resolvePubChemFallback(context.Background(), results, true)
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

func TestResolvePubChemFallbackNotFoundSetsMessage(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "999999999")
	withMockPCBTransport(t, func(r *http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, `{"PropertyTable":{"Properties":[]}}`), nil
	})

	results := []*model.SingleResult{missResult("pubchem_id", "999999999")}
	_, _, notFound, _, _ := resolvePubChemFallback(context.Background(), results, true)
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
	_, _, _, failed, _ := resolvePubChemFallback(context.Background(), results, true)
	if failed != 1 {
		t.Fatalf("expected 1 failed, got %d", failed)
	}
	if results[0].ErrMsg != "No local match; PubChem live lookup unavailable" {
		t.Errorf("unexpected ErrMsg: %q", results[0].ErrMsg)
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

	attempted, _, _, _, skipped := resolvePubChemFallback(context.Background(), results, true)
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

	attempted, _, _, _, skipped := resolvePubChemFallback(context.Background(), results, true)
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
