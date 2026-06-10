package api

import (
	"bytes"
	"context"
	"ctslite/model"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockClassyFire replaces classyFireFetcher for tests
func mockClassyFire(t *testing.T, fn func(string) (cfbFetch, error)) {
	t.Helper()
	orig := classyFireFetcher
	classyFireFetcher = fn
	t.Cleanup(func() { classyFireFetcher = orig })
	resetCfbPacing(t)
}

// resetCfbPacing zeroes the pacing/retry delays
func resetCfbPacing(t *testing.T) {
	t.Helper()
	origHit, origBurst, origSteady := cfbHitDelay, cfbBurstGap, cfbSteadyGap
	origPause, origLong, origRetry := cfb429Pause, cfb429PauseLong, cfbRetryDelays
	cfbHitDelay, cfbBurstGap, cfbSteadyGap = 0, 0, 0
	cfb429Pause, cfb429PauseLong = 0, 0
	cfbRetryDelays = []time.Duration{0, 0}

	cfbGateMu.Lock()
	cfbNextAllowed, cfbLastReqAt = time.Time{}, time.Time{}
	cfbMissGap, cfb429Streak = 0, 0
	cfbGateMu.Unlock()
	atomic.StoreInt64(&cfbActiveRequests, 0)

	t.Cleanup(func() {
		cfbHitDelay, cfbBurstGap, cfbSteadyGap = origHit, origBurst, origSteady
		cfb429Pause, cfb429PauseLong, cfbRetryDelays = origPause, origLong, origRetry
	})
}

func fakeClassyFireInfo() *model.ClassyFireInfo {
	return &model.ClassyFireInfo{
		Kingdom:      "Organic compounds",
		Superclass:   "Organoheterocyclic compounds",
		Class:        "Imidazopyrimidines",
		Subclass:     "Purines and purine derivatives",
		DirectParent: "Xanthines",
		Description:  "A xanthine alkaloid.",
	}
}

func TestEnrichWithClassyFireAttachesInfo(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	results := []*model.SingleResult{
		{
			MatchFound: true,
			Matches:    []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}},
		},
	}
	enrichWithClassyFire(context.Background(), results)

	cf := results[0].Matches[0].ClassyFire
	if cf == nil {
		t.Fatal("expected ClassyFire info to be attached, got nil")
	}
	if cf.Kingdom != "Organic compounds" {
		t.Errorf("Kingdom: want %q, got %q", "Organic compounds", cf.Kingdom)
	}
	if cf.DirectParent != "Xanthines" {
		t.Errorf("DirectParent: want %q, got %q", "Xanthines", cf.DirectParent)
	}
}

// Two results share the same InChIKey, fetcher should only be called once
func TestEnrichWithClassyFireDeduplicatesInChIKeys(t *testing.T) {
	callCount := 0
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		callCount++
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	sharedKey := "RYYVLZVUVIJVGH-UHFFFAOYSA-N"
	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: sharedKey}}},
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: sharedKey}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if callCount != 1 {
		t.Errorf("expected 1 ClassyFire call for duplicate InChIKey, got %d", callCount)
	}
	// Both compounds should have the info attached
	for i, r := range results {
		if r.Matches[0].ClassyFire == nil {
			t.Errorf("result[%d]: expected ClassyFire info, got nil", i)
		}
	}
}

// A single query that matches more than cfbMaxMatchesPerQuery compounds (top hit only disabled)
// Only the first cfbMaxMatchesPerQuery should be classified, the rest get the note
func TestEnrichWithClassyFireCapsMatchesPerQuery(t *testing.T) {
	var classified []string
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		classified = append(classified, inchikey)
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	// Manufacture a list of 5 matches for a query
	matches := make([]*model.Compound, 5)
	for i := range matches {
		matches[i] = &model.Compound{InChIKey: string(rune('A'+i)) + "YYVLZVUVIJVGH-UHFFFAOYSA-N"}
	}
	results := []*model.SingleResult{{MatchFound: true, Matches: matches}}

	enrichWithClassyFire(context.Background(), results)

	if len(classified) != cfbMaxMatchesPerQuery {
		t.Errorf("expected %d ClassyFire calls for one query, got %d", cfbMaxMatchesPerQuery, len(classified))
	}

	// Check that matches were properly classified or noted
	for i, c := range matches {
		if i < cfbMaxMatchesPerQuery {
			if c.ClassyFire == nil || c.ClassyFire.Error != "" {
				t.Errorf("match[%d]: expected classification, got %+v", i, c.ClassyFire)
			}
		} else {
			if c.ClassyFire == nil || c.ClassyFire.Error != cfbCappedNote {
				t.Errorf("match[%d]: expected capped note, got %+v", i, c.ClassyFire)
			}
		}
	}
}

// A query with no matches should not call ClassyFire
func TestEnrichWithClassyFireSkipsNoMatches(t *testing.T) {
	called := false
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		called = true
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	results := []*model.SingleResult{
		{MatchFound: false, Matches: nil},
	}
	enrichWithClassyFire(context.Background(), results)

	if called {
		t.Error("expected ClassyFire fetcher not to be called when there are no matches")
	}
}

// Compound not in the ClassyFire database, error should be clearly surfaced
func TestEnrichWithClassyFireSurfacesNotFound(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: &model.ClassyFireInfo{Error: "Not found in ClassyFire"}}, nil
	})

	// Manufacture a fake inchikey but pretend it was a match
	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "AAAAAAAAAAAAAA-AAAAAAAAAA-A"}}},
	}
	enrichWithClassyFire(context.Background(), results)

	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error != "Not found in ClassyFire" {
		t.Errorf("expected not-found error surfaced, got %+v", cf)
	}
}

// Non-429 errors should surface in the result and not panic
func TestEnrichWithClassyFireSurfacesFetchError(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"}, errors.New("connection refused")
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results) // must not panic

	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error != "ClassyFire service unreachable" {
		t.Errorf("expected fetch error surfaced, got %+v", cf)
	}
}

// Transient failure twice, then success. Should perform 3 attempts and not give up
func TestEnrichWithClassyFireRetriesThenSucceeds(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return cfbFetch{mode: cfbRetryLimited}, errors.New("transient error")
		}
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (2 retries), got %d", got)
	}
	if results[0].Matches[0].ClassyFire == nil {
		t.Error("expected ClassyFire info to be attached after successful retry")
	}
}

// Give up after 3 transient failures
func TestEnrichWithClassyFireGivesUpAfterRetries(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		atomic.AddInt32(&calls, 1)
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire unavailable (HTTP 503)"}, errors.New("HTTP 503")
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts before giving up, got %d", got)
	}
	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error != "ClassyFire unavailable (HTTP 503)" {
		t.Errorf("expected error surfaced after exhausting retries, got %+v", cf)
	}
}

func TestClassyFireQueueDepthTracksActiveRequests(t *testing.T) {
	var duringDepth int64
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		duringDepth = cfbQueueDepth()
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	if d := cfbQueueDepth(); d != 0 {
		t.Fatalf("expected depth 0 before request, got %d", d)
	}

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "QNAYBMKLOCPYGJ-REOHCLBHSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if duringDepth != 1 {
		t.Errorf("expected depth 1 during classification, got %d", duringDepth)
	}
	if d := cfbQueueDepth(); d != 0 {
		t.Errorf("expected depth back to 0 after request, got %d", d)
	}
}

func TestClassyFireQueueDepthCountsConcurrentRequests(t *testing.T) {
	const n = 4
	release := make(chan struct{})
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		<-release // hold every request in the queue until the test releases them
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		key := string(rune('A'+i)) + "YYVLZVUVIJVGH-UHFFFAOYSA-N"
		go func(k string) {
			defer wg.Done()
			results := []*model.SingleResult{{MatchFound: true, Matches: []*model.Compound{{InChIKey: k}}}}
			enrichWithClassyFire(context.Background(), results)
		}(key)
	}

	// Wait until all n requests have entered the queue, then observe the depth
	waitForQueueDepth(t, n)
	got := cfbQueueDepth()
	close(release)
	wg.Wait()

	if got != n {
		t.Errorf("expected queue depth %d with %d concurrent requests, got %d", n, n, got)
	}
	if d := cfbQueueDepth(); d != 0 {
		t.Errorf("expected depth back to 0 after all requests finished, got %d", d)
	}
}

func TestClassyFireQueueDepthDropsWhenClientDisconnects(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		close(entered)  // we're now mid-classification, in the queue
		<-release       // stay in the queue until the test lets go
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		results := []*model.SingleResult{{MatchFound: true, Matches: []*model.Compound{{InChIKey: "QNAYBMKLOCPYGJ-REOHCLBHSA-N"}}}}
		enrichWithClassyFire(ctx, results)
	}()

	<-entered
	if d := cfbQueueDepth(); d != 1 {
		t.Fatalf("expected depth 1 while request is in flight, got %d", d)
	}

	cancel()        // client closes the tab
	close(release)  // unblock the in-flight fetch so the goroutine can unwind
	<-done

	if d := cfbQueueDepth(); d != 0 {
		t.Errorf("expected depth 0 after the client disconnected, got %d", d)
	}
}

// Client disconnect during classification should log properly
func TestStreamClassyFireLogsClientDisconnect(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	var logBuf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(orig) })

	ctx, cancel := context.WithCancel(context.Background())
	keys := []string{
		"AYYVLZVUVIJVGH-UHFFFAOYSA-N",
		"BYYVLZVUVIJVGH-UHFFFAOYSA-N",
		"CYYVLZVUVIJVGH-UHFFFAOYSA-N",
	}
	var got int
	streamClassyFire(ctx, keys, func(key string, info *model.ClassyFireInfo) {
		got++
		cancel() // client disconnects after the first result resolves
	})

	if got != 1 {
		t.Errorf("expected to stop after 1 result, processed %d", got)
	}
	if want := "ClassyFire request cancelled by client; stopping after 1 of 3 compounds"; !strings.Contains(logBuf.String(), want) {
		t.Errorf("expected log to contain %q, got: %q", want, logBuf.String())
	}
}

// waitForQueueDepth blocks until the queue depth reaches 'want' or the test times out
func waitForQueueDepth(t *testing.T, want int64) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if cfbQueueDepth() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth never reached %d (last saw %d)", want, cfbQueueDepth())
}

// A cached key should not be fetched from classyfire
func TestClassyFireServesInProcessCacheWithoutFetching(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		t.Fatalf("fetcher must not be called for an in-process-cached key: %s", inchikey)
		return cfbFetch{}, nil
	})

	key := "RYYVLZVUVIJVGH-UHFFFAOYSA-N"
	cfbCache.Store(key, cfbCacheEntry{info: fakeClassyFireInfo(), expires: time.Now().Add(time.Hour)})
	t.Cleanup(func() { cfbCache.Delete(key) })

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: key}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Kingdom != "Organic compounds" {
		t.Errorf("expected the cached classification, got %+v", cf)
	}
}

// Test the fast-fail behavior when classyfire is down
func TestEnrichWithClassyFireGivesUpWhenServiceDown(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		atomic.AddInt32(&calls, 1)
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"}, errors.New("down")
	})

	const numKeys = 10
	results := make([]*model.SingleResult, numKeys)
	for i := range results {
		key := string(rune('A'+i)) + "YYVLZVUVIJVGH-UHFFFAOYSA-N"
		results[i] = &model.SingleResult{MatchFound: true, Matches: []*model.Compound{{InChIKey: key}}}
	}
	enrichWithClassyFire(context.Background(), results)

	// Only the first key probes (cfbDownGiveUp attempts), the rest are fast-failed
	if got := atomic.LoadInt32(&calls); got != int32(cfbDownGiveUp) {
		t.Errorf("expected %d total fetches before giving up, got %d", cfbDownGiveUp, got)
	}

	// Every compound is still reported with an error rather than left unclassified
	for i, r := range results {
		cf := r.Matches[0].ClassyFire
		if cf == nil || cf.Error == "" {
			t.Errorf("result[%d]: expected an error, got %+v", i, cf)
		}
	}

	// Keys after the breaker tripped carry the generic "unavailable" note
	if cf := results[numKeys-1].Matches[0].ClassyFire; cf == nil || cf.Error != cfbUnavailableNote {
		t.Errorf("expected last compound to carry %q, got %+v", cfbUnavailableNote, cf)
	}
}

// Consecutive 429s are treated differently to non-429s, retry more than cfbDownGiveUp times
func TestEnrichWithClassyFireRetriesRateLimitBeyondNormalLimit(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		if atomic.AddInt32(&calls, 1) <= 4 {
			return cfbFetch{mode: cfbRetryRateLimited, errMsg: "ClassyFire rate limited (HTTP 429)"}, errors.New("HTTP 429")
		}
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results)

	if got := atomic.LoadInt32(&calls); got != 5 {
		t.Errorf("expected 5 attempts (4 rate-limited retries then success), got %d", got)
	}
	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error != "" {
		t.Errorf("expected successful classification after rate-limit retries, got %+v", cf)
	}
}

// If cfb429MaxRetries is exceeded, the InChIKey is flagged as failed
func TestEnrichWithClassyFireRateLimitGivesUpAfterRetries(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		atomic.AddInt32(&calls, 1)
		return cfbFetch{mode: cfbRetryRateLimited, errMsg: "ClassyFire rate limited (HTTP 429)"}, errors.New("HTTP 429")
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(context.Background(), results) // must terminate, not hang

	if got := atomic.LoadInt32(&calls); got != cfb429MaxRetries {
		t.Errorf("expected %d attempts before giving up, got %d", cfb429MaxRetries, got)
	}
	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error != "ClassyFire rate limited (HTTP 429)" {
		t.Errorf("expected rate-limit error surfaced after retries, got %+v", cf)
	}
}

// Test the miss gaps of a cache miss
func TestEnrichWithClassyFirePacesByCacheHeader(t *testing.T) {
	resetCfbPacing(t)
	cfbHitDelay = 5 * time.Millisecond
	cfbBurstGap = 80 * time.Millisecond
	cfbGateMu.Lock()
	cfbMissGap = cfbBurstGap // current miss gap starts at the burst gap
	cfbGateMu.Unlock()

	hits := map[string]bool{"AAAAAAAAAAAAAA-BBBBBBBBBB-A": false, "CCCCCCCCCCCCCC-DDDDDDDDDD-B": true}
	orig := classyFireFetcher
	classyFireFetcher = func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: hits[inchikey]}, nil
	}
	t.Cleanup(func() { classyFireFetcher = orig })

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{
			{InChIKey: "AAAAAAAAAAAAAA-BBBBBBBBBB-A"}, // MISS first
			{InChIKey: "CCCCCCCCCCCCCC-DDDDDDDDDD-B"}, // HIT second
		}},
	}

	start := time.Now()
	enrichWithClassyFire(context.Background(), results)
	elapsed := time.Since(start)

	// The pause after the first MISS is the miss gap
	if elapsed < cfbBurstGap {
		t.Errorf("expected at least the miss gap (%s) between requests, took %s", cfbBurstGap, elapsed)
	}
}

// Realizing client-disconnect cancellation early
func TestStreamClassyFireCancels(t *testing.T) {
	var calls int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		atomic.AddInt32(&calls, 1)
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	var emitted int
	streamClassyFire(ctx, []string{"A", "B", "C"}, func(key string, info *model.ClassyFireInfo) {
		emitted++
	})

	if emitted != 0 {
		t.Errorf("expected no results emitted after cancellation, got %d", emitted)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("expected fetcher not called after cancellation, got %d calls", got)
	}
}

// Test round-robin behavior of cfb queue across concurrent requests
func TestStreamClassyFireInterleavesConcurrentBatches(t *testing.T) {
	resetCfbPacing(t)
	cfbHitDelay = 3 * time.Millisecond // small gap to force interleaving
	orig := classyFireFetcher
	classyFireFetcher = func(string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	}
	t.Cleanup(func() { classyFireFetcher = orig })

	var mu sync.Mutex
	var order []string
	record := func(label string) func(string, *model.ClassyFireInfo) {
		return func(string, *model.ClassyFireInfo) {
			mu.Lock()
			order = append(order, label)
			mu.Unlock()
		}
	}
	keys := []string{"k", "k", "k", "k", "k"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); streamClassyFire(context.Background(), keys, record("A")) }()
	go func() { defer wg.Done(); streamClassyFire(context.Background(), keys, record("B")) }()
	wg.Wait()

	// Assign when the first B was classified vs the last A
	firstB, lastA := -1, -1
	for i, label := range order {
		if label == "B" && firstB < 0 {
			firstB = i
		}
		if label == "A" {
			lastA = i
		}
	}
	if firstB < 0 || lastA < 0 {
		t.Fatalf("expected both batches to run, order=%v", order)
	}
	if firstB > lastA {
		t.Errorf("batch B was starved until A finished (order=%v)", order)
	}
}

// --- defaultClassyFireFetcher HTTP-layer tests ---
// These exercise the real fetcher by swapping cfbHTTPClient for a mock

// mockRoundTripper lets a test serve canned HTTP responses to defaultClassyFireFetcher
type mockRoundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (m mockRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return m.fn(r) }

// withMockTransport swaps cfbHTTPClient for one backed by fn, restoring it after the test
func withMockTransport(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	orig := cfbHTTPClient
	cfbHTTPClient = &http.Client{Transport: mockRoundTripper{fn}}
	t.Cleanup(func() { cfbHTTPClient = orig })
}

// cfbResp builds a canned *http.Response for the mock transport
func cfbResp(status int, body string, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     h,
	}
}

// freshKey returns a unique key and clears any cached entry
func freshKey(t *testing.T, key string) string {
	t.Helper()
	cfbCache.Delete(key)
	t.Cleanup(func() { cfbCache.Delete(key) })
	return key
}

// A full 200 response should map every taxonomy field and get cached
func TestDefaultFetcherParsesFullTaxonomy(t *testing.T) {
	key := freshKey(t, "FULLTAXON0001-AAAAAAAAAA-N")
	body := `{
		"kingdom":{"name":"Organic compounds"},
		"superclass":{"name":"Organoheterocyclic compounds"},
		"class":{"name":"Imidazopyrimidines"},
		"subclass":{"name":"Purines and purine derivatives"},
		"direct_parent":{"name":"Xanthines"},
		"description":"A xanthine alkaloid."
	}`
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusOK, body, map[string]string{"X-Cache": "HIT"}), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.mode != cfbTerminal {
		t.Errorf("mode: want cfbTerminal, got %v", res.mode)
	}
	if !res.cacheHit {
		t.Error("expected cacheHit=true from X-Cache: HIT header")
	}
	if res.info == nil {
		t.Fatal("expected info, got nil")
	}
	if res.info.Error != "" {
		t.Errorf("unexpected error field: %q", res.info.Error)
	}
	if res.info.Kingdom != "Organic compounds" {
		t.Errorf("Kingdom: want %q, got %q", "Organic compounds", res.info.Kingdom)
	}
	if res.info.DirectParent != "Xanthines" {
		t.Errorf("DirectParent: want %q, got %q", "Xanthines", res.info.DirectParent)
	}
	if res.info.Description != "A xanthine alkaloid." {
		t.Errorf("Description: want %q, got %q", "A xanthine alkaloid.", res.info.Description)
	}
	// A successful fetch must populate the in-process cache
	if cached, ok := cfbCacheLookup(key); !ok || cached.Kingdom != "Organic compounds" {
		t.Errorf("expected classification cached, got %+v (ok=%v)", cached, ok)
	}
}

// A body with a description but no taxonomy means the compound is unclassified
func TestDefaultFetcherDescriptionOnlyIsUnclassified(t *testing.T) {
	key := freshKey(t, "DESCONLY00001-AAAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		// Body parses but carries no taxonomy nodes.
		return cfbResp(http.StatusOK, `{"description":"only a description"}`, nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.info == nil || res.info.Error != "No classification available" {
		t.Errorf("expected 'No classification available', got %+v", res.info)
	}
}

// An empty JSON object means the compound is unclassified
func TestDefaultFetcherEmptyBodyIsUnclassified(t *testing.T) {
	key := freshKey(t, "EMPTYBODY0001-AAAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusOK, `{}`, nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.info == nil || res.info.Error != "No classification available" {
		t.Errorf("expected 'No classification available' for empty body, got %+v", res.info)
	}
}

// A 404 is a terminal "not found" answer and should be cached
func TestDefaultFetcherNotFound(t *testing.T) {
	key := freshKey(t, "NOTFOUND00001-AAAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusNotFound, "", nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.mode != cfbTerminal {
		t.Errorf("404 should be terminal, got mode %v", res.mode)
	}
	if res.info == nil || res.info.Error != "Not found in ClassyFire" {
		t.Errorf("expected 'Not found in ClassyFire', got %+v", res.info)
	}
	// 404 is a terminal answer and must be cached so we don't re-query.
	if _, ok := cfbCacheLookup(key); !ok {
		t.Error("expected 404 result to be cached")
	}
}

// A 429 is flagged as rate-limited and must not be cached
func TestDefaultFetcherRateLimited(t *testing.T) {
	key := freshKey(t, "RATELIMIT0001-AAAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusTooManyRequests, "", nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err == nil {
		t.Error("expected error for HTTP 429")
	}
	if res.mode != cfbRetryRateLimited {
		t.Errorf("mode: want cfbRetryRateLimited, got %v", res.mode)
	}
	if !strings.Contains(res.errMsg, "429") {
		t.Errorf("errMsg should mention 429, got %q", res.errMsg)
	}
	// A 429 is transient and must not be cached.
	if _, ok := cfbCacheLookup(key); ok {
		t.Error("429 must not be cached")
	}
}

// A 5xx is flagged as a transient (retry-limited) failure
func TestDefaultFetcherServerError(t *testing.T) {
	key := freshKey(t, "SERVERERR0001-AAAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusServiceUnavailable, "", nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err == nil {
		t.Error("expected error for HTTP 503")
	}
	if res.mode != cfbRetryLimited {
		t.Errorf("mode: want cfbRetryLimited, got %v", res.mode)
	}
	if !strings.Contains(res.errMsg, "503") {
		t.Errorf("errMsg should mention 503, got %q", res.errMsg)
	}
}

// An unparseable 200 body is treated as a transient failure
func TestDefaultFetcherMalformedJSON(t *testing.T) {
	key := freshKey(t, "MALFORMED00001-AAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusOK, "this is not json", nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
	if res.mode != cfbRetryLimited {
		t.Errorf("mode: want cfbRetryLimited, got %v", res.mode)
	}
	if res.errMsg != "ClassyFire response unreadable" {
		t.Errorf("errMsg: want %q, got %q", "ClassyFire response unreadable", res.errMsg)
	}
}

// A network/transport failure is reported as service-unreachable
func TestDefaultFetcherTransportError(t *testing.T) {
	key := freshKey(t, "TRANSPORT00001-AAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	res, err := defaultClassyFireFetcher(key)
	if err == nil {
		t.Error("expected error when transport fails")
	}
	if res.mode != cfbRetryLimited {
		t.Errorf("mode: want cfbRetryLimited, got %v", res.mode)
	}
	if res.errMsg != "ClassyFire service unreachable" {
		t.Errorf("errMsg: want %q, got %q", "ClassyFire service unreachable", res.errMsg)
	}
}

// A cached key is served from memory without any HTTP call
func TestDefaultFetcherServesInProcessCacheWithoutHTTP(t *testing.T) {
	key := "CACHEDKEY00001-AAAAAAAAA-N"
	cfbCache.Store(key, cfbCacheEntry{info: fakeClassyFireInfo(), expires: time.Now().Add(time.Hour)})
	t.Cleanup(func() { cfbCache.Delete(key) })

	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatalf("transport must not be called for a cached key")
		return nil, nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.cacheHit {
		t.Error("expected cacheHit=true for in-process cache")
	}
	if res.info == nil || res.info.Kingdom != "Organic compounds" {
		t.Errorf("expected cached classification, got %+v", res.info)
	}
}

// Guard against a regression where a node with an empty name still counts as a classification
func TestDefaultFetcherEmptyNamedNodesAreUnclassified(t *testing.T) {
	key := freshKey(t, "EMPTYNODE00001-AAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusOK, `{"kingdom":{"name":""},"description":""}`, nil), nil
	})

	res, err := defaultClassyFireFetcher(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.info == nil || res.info.Error != "No classification available" {
		t.Errorf("expected unclassified for empty node names, got %+v", res.info)
	}
}

// The fetcher must never run concurrently
func TestEnrichWithClassyFireIsSequential(t *testing.T) {
	var inFlight int32
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		if atomic.AddInt32(&inFlight, 1) > 1 {
			t.Error("fetcher called concurrently; expected serialized requests")
		}
		atomic.AddInt32(&inFlight, -1)
		return cfbFetch{info: fakeClassyFireInfo(), cacheHit: true}, nil
	})

	makeResults := func() []*model.SingleResult {
		return []*model.SingleResult{
			{MatchFound: true, Matches: []*model.Compound{
				{InChIKey: "AAAAAAAAAAAAAA-BBBBBBBBBB-A"},
				{InChIKey: "CCCCCCCCCCCCCC-DDDDDDDDDD-B"},
			}},
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			enrichWithClassyFire(context.Background(), makeResults())
		}()
	}
	wg.Wait()
}
