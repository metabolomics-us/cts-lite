package api

import (
	"bytes"
	"context"
	"ctslite/model"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockClassyFire replaces both routes (cfb and Wishart directly) with fn, so a failing
// mock means ClassyFire as a whole fails. Mocks only care about the key, so the
// context is dropped here rather than in every mock
func mockClassyFire(t *testing.T, fn func(string) (cfbFetch, error)) {
	t.Helper()
	mockRoutes(t, fn, fn)
}

// mockRoutes replaces classyFireFetcher (cfb) and wishartFetcher (Wishart directly) separately
func mockRoutes(t *testing.T, cfbFn, wishartFn func(string) (cfbFetch, error)) {
	t.Helper()
	origCFB, origWishart := classyFireFetcher, wishartFetcher
	classyFireFetcher = func(_ context.Context, key string) (cfbFetch, error) { return cfbFn(key) }
	wishartFetcher = func(_ context.Context, key string) (cfbFetch, error) { return wishartFn(key) }
	t.Cleanup(func() { classyFireFetcher, wishartFetcher = origCFB, origWishart })
	resetCfbPacing(t)
}

// resetCfbPacing zeroes the pacing/retry delays and clears the gate state
func resetCfbPacing(t *testing.T) {
	t.Helper()
	origHit, origFloor, origRefill, origCooldown := cfbHitDelay, cfbMissFloor, cfbSlotRefill, cfb429Cooldown
	origRetry, origDownFor := cfbRetryDelays, cfbRouteDownFor
	cfbHitDelay, cfbMissFloor, cfbSlotRefill, cfb429Cooldown = 0, 0, 0, 0
	cfbRetryDelays, cfbRouteDownFor = []time.Duration{0, 0}, 0

	cfbGateMu.Lock()
	cfbNextAllowed, cfbSlotsAt, cfbSlots = time.Time{}, time.Time{}, 0
	cfbCooldownUntil, cfbSkipUntil, wishartSkipUntil = time.Time{}, time.Time{}, time.Time{}
	cfbFailStreak, wishartFailStreak = 0, 0
	cfbGateMu.Unlock()
	atomic.StoreInt64(&cfbActiveRequests, 0)

	// The known-good key check answers "up" without any HTTP unless a test says otherwise
	origCheck := cfbCheckProbe
	cfbCheckProbe = func(context.Context) cfbCheck { return cfbCheckUp }
	cfbCheckMu.Lock()
	cfbCheckUntil = time.Time{}
	cfbCheckMu.Unlock()

	t.Cleanup(func() {
		cfbHitDelay, cfbMissFloor, cfbSlotRefill, cfb429Cooldown = origHit, origFloor, origRefill, origCooldown
		cfbRetryDelays, cfbRouteDownFor = origRetry, origDownFor
		cfbCheckProbe = origCheck
		cfbCheckMu.Lock()
		cfbCheckUntil = time.Time{}
		cfbCheckMu.Unlock()
	})
}

// mockServiceCheck replaces the known-good key check and counts how often it runs
func mockServiceCheck(t *testing.T, result cfbCheck) *int32 {
	t.Helper()
	var n int32
	cfbCheckProbe = func(context.Context) cfbCheck {
		atomic.AddInt32(&n, 1)
		return result
	}
	return &n
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

// Transient failure on cfb and then Wishart, then success when cfb is retried. Should not give up
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
		t.Errorf("expected 3 calls (cfb, Wishart, then cfb on the retry), got %d", got)
	}
	if results[0].Matches[0].ClassyFire == nil {
		t.Error("expected ClassyFire info to be attached after successful retry")
	}
}

// Give up after 3 transient failures, each attempt asks cfb and then Wishart
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

	if got := atomic.LoadInt32(&calls); got != 2*3 {
		t.Errorf("expected 3 attempts (6 calls) before giving up, got %d calls", got)
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
	streamClassyFire(ctx, cfbKeyChan(keys), func(key string, info *model.ClassyFireInfo) {
		got++
		cancel() // client disconnects after the first result resolves
	})

	if got != 1 {
		t.Errorf("expected to stop after 1 result, processed %d", got)
	}
	if want := "ClassyFire request cancelled by client; stopping after 1 compounds"; !strings.Contains(logBuf.String(), want) {
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

	// Only the first key probes (cfbDownGiveUp attempts on cfb and Wishart), the rest are fast-failed
	if got := atomic.LoadInt32(&calls); got != int32(2*cfbDownGiveUp) {
		t.Errorf("expected %d total fetches before giving up, got %d", 2*cfbDownGiveUp, got)
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

// serverError is a 500 answer from either route
func serverError(string) (cfbFetch, error) {
	return cfbFetch{mode: cfbRetry500, errMsg: cfbInternalErrorNote}, errors.New("HTTP 500")
}

// A key that 500s on cfb and Wishart fails after that one attempt, a 500 key always 500s.
// The known-good key check answers, so the 500 is cached and the breaker never trips
func TestEnrichWithClassyFire500SingleKeyFailsWithoutRetry(t *testing.T) {
	key := freshKey(t, "FIVEHUNDRED001-UHFFFAOYSA-N")
	var calls int32
	mockClassyFire(t, counted(&calls, serverError))
	checks := mockServiceCheck(t, cfbCheckUp)

	results := singleKeyResults(key)
	enrichWithClassyFire(context.Background(), results)

	// One attempt, cfb and then Wishart
	if calls != 2 || *checks != 1 {
		t.Errorf("expected 2 calls and 1 check on a persistent 500, got %d and %d", calls, *checks)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != cfbInternalErrorNote {
		t.Errorf("expected 500 error surfaced, got %+v", cf)
	}
	if cached, ok := cfbCacheLookup(key); !ok || cached.Error != cfbInternalErrorNote {
		t.Errorf("expected Wishart's 500 cached, got %+v (ok=%v)", cached, ok)
	}
}

// A failed known-good key check means ClassyFire is down: the breaker trips after the first 500
func TestEnrichWithClassyFire500WithFailedCheckTripsBreaker(t *testing.T) {
	var calls int32
	mockClassyFire(t, counted(&calls, serverError))
	mockServiceCheck(t, cfbCheckDown)

	results := singleKeyResults("AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N", "CYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if calls != 2 {
		t.Errorf("expected 2 calls before the breaker tripped, got %d", calls)
	}
	if cf := results[2].Matches[0].ClassyFire; cf == nil || cf.Error != cfbUnavailableNote {
		t.Errorf("expected last compound to carry %q, got %+v", cfbUnavailableNote, cf)
	}
	if _, ok := cfbCacheLookup("AYYVLZVUVIJVGH-UHFFFAOYSA-N"); ok {
		t.Error("a 500 while ClassyFire is down must not be cached")
	}
}

// Rerunning a batch whose good keys are cached must not trip the breaker on its 500 keys:
// one check covers them all and a second rerun serves them from the cache
func TestEnrichWithClassyFire500KeysBetweenCachedKeysDoNotTrip(t *testing.T) {
	bad := []string{freshKey(t, "BADKEYAAAAAAAA-UHFFFAOYSA-N"), freshKey(t, "BADKEYBBBBBBBB-UHFFFAOYSA-N"), freshKey(t, "BADKEYCCCCCCCC-UHFFFAOYSA-N")}
	var keys []string
	for i, b := range bad {
		good := freshKey(t, string(rune('A'+i))+"GOODKEYAAAAAA-UHFFFAOYSA-N")
		cfbCache.Store(good, cfbCacheEntry{fakeClassyFireInfo(), time.Now().Add(time.Hour)})
		keys = append(keys, good, b)
	}
	var calls int32
	mockClassyFire(t, counted(&calls, serverError))
	checks := mockServiceCheck(t, cfbCheckUp)

	for run := 1; run <= 2; run++ {
		results := singleKeyResults(keys...)
		enrichWithClassyFire(context.Background(), results)
		for i := 1; i < len(keys); i += 2 {
			if cf := results[i].Matches[0].ClassyFire; cf == nil || cf.Error != cfbInternalErrorNote {
				t.Errorf("run %d, %s: expected its own 500, got %+v", run, keys[i], cf)
			}
		}
	}
	if calls != 2*int32(len(bad)) || *checks != 1 {
		t.Errorf("expected %d calls and 1 check over both runs, got %d and %d", 2*len(bad), calls, *checks)
	}
}

// A 500 only cfb gave, with Wishart unreachable, may be cfb failing on its own, so it is not cached
func TestEnrichWithClassyFireCfbOnly500IsNotCached(t *testing.T) {
	key := freshKey(t, "CFBONLY500AAAA-UHFFFAOYSA-N")
	mockRoutes(t, serverError, unreachable)

	results := singleKeyResults(key)
	enrichWithClassyFire(context.Background(), results)

	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != cfbInternalErrorNote {
		t.Errorf("expected cfb's 500 surfaced, got %+v", cf)
	}
	if _, ok := cfbCacheLookup(key); ok {
		t.Error("a cfb-only 500 must not be cached")
	}
}

// A check that only got 429s cannot tell, so the key fails uncached and nothing trips
func TestEnrichWithClassyFire500WithUnknownCheckIsNotCached(t *testing.T) {
	key := freshKey(t, "UNKNOWNCHK0001-UHFFFAOYSA-N")
	mockClassyFire(t, serverError)
	mockServiceCheck(t, cfbCheckUnknown)

	results := singleKeyResults(key, "BYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if _, ok := cfbCacheLookup(key); ok {
		t.Error("a 500 must not be cached when the check could not tell")
	}
	if cf := results[1].Matches[0].ClassyFire; cf == nil || cf.Error != cfbInternalErrorNote {
		t.Errorf("expected the next key looked up, not fast-failed, got %+v", cf)
	}
}

// The known-good key check asks Wishart directly, and cfb only when Wishart cannot be reached
func TestCfbCheckProbeAsksWishartFirst(t *testing.T) {
	probe := cfbCheckProbe // the real check, before resetCfbPacing mocks it
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, classified), counted(&wishartCalls, classified))
	t.Cleanup(func() { cfbCache.Delete(cfbHealthProbeKey) })

	if got := probe(context.Background()); got != cfbCheckUp || cfbCalls != 0 || wishartCalls != 1 {
		t.Errorf("expected up from Wishart alone, got %v with %d cfb and %d Wishart calls", got, cfbCalls, wishartCalls)
	}

	cfbCalls, wishartCalls = 0, 0
	mockRoutes(t, counted(&cfbCalls, classified), counted(&wishartCalls, unreachable))
	if got := probe(context.Background()); got != cfbCheckUp || cfbCalls != 1 {
		t.Errorf("expected up from cfb when Wishart is unreachable, got %v with %d cfb calls", got, cfbCalls)
	}
}

// A cfb 429 followed by Wishart's 500 is final, not retried as a cfb 429
func TestEnrichWithClassyFireCfb429ThenWishart500FailsAtOnce(t *testing.T) {
	key := freshKey(t, "CFB429WISH5000-UHFFFAOYSA-N")
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, rateLimited), counted(&wishartCalls, serverError))

	results := singleKeyResults(key)
	enrichWithClassyFire(context.Background(), results)

	if cfbCalls != 1 || wishartCalls != 1 {
		t.Errorf("expected 1 cfb and 1 Wishart call, got %d and %d", cfbCalls, wishartCalls)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != cfbInternalErrorNote {
		t.Errorf("expected Wishart's 500 surfaced, got %+v", cf)
	}
}

// rateLimited is a 429 answer from either route
func rateLimited(string) (cfbFetch, error) {
	return cfbFetch{mode: cfbRetryRateLimited, errMsg: "ClassyFire rate limited (HTTP 429)"}, errors.New("HTTP 429")
}

// unreachable is a network failure from either route
func unreachable(string) (cfbFetch, error) {
	return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"}, errors.New("down")
}

// counted wraps a mock so the test can see how often a route was asked
func counted(n *int32, fn func(string) (cfbFetch, error)) func(string) (cfbFetch, error) {
	return func(key string) (cfbFetch, error) {
		atomic.AddInt32(n, 1)
		return fn(key)
	}
}

func classified(string) (cfbFetch, error) { return cfbFetch{info: fakeClassyFireInfo()}, nil }

// singleKeyResults builds one matched query per key
func singleKeyResults(keys ...string) []*model.SingleResult {
	results := make([]*model.SingleResult, len(keys))
	for i, k := range keys {
		results[i] = &model.SingleResult{MatchFound: true, Matches: []*model.Compound{{InChIKey: k}}}
	}
	return results
}

// A cfb 429 sends the same key to Wishart directly instead of waiting for cfb
func TestEnrichWithClassyFireRateLimitedCfbFallsBackToWishart(t *testing.T) {
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, rateLimited), counted(&wishartCalls, classified))

	results := singleKeyResults("RYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if cfbCalls != 1 || wishartCalls != 1 {
		t.Errorf("expected 1 cfb and 1 Wishart call, got %d and %d", cfbCalls, wishartCalls)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != "" {
		t.Errorf("expected classification from Wishart, got %+v", cf)
	}
}

// While cfb cools down after a 429 the next key goes straight to Wishart
func TestEnrichWithClassyFireCfbCooldownSendsNextKeyDirect(t *testing.T) {
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, rateLimited), counted(&wishartCalls, classified))
	cfb429Cooldown = time.Hour

	enrichWithClassyFire(context.Background(), singleKeyResults("AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N"))

	if cfbCalls != 1 || wishartCalls != 2 {
		t.Errorf("expected 1 cfb and 2 Wishart calls, got %d and %d", cfbCalls, wishartCalls)
	}
}

// A 429 from Wishart directly retries there and gives up after cfbDirect429MaxRetries
func TestEnrichWithClassyFireDirectRateLimitGivesUpAfterRetries(t *testing.T) {
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, rateLimited), counted(&wishartCalls, rateLimited))

	results := singleKeyResults("RYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results) // must terminate, not hang

	if cfbCalls != 1 || wishartCalls != cfbDirect429MaxRetries {
		t.Errorf("expected 1 cfb and %d Wishart calls, got %d and %d", cfbDirect429MaxRetries, cfbCalls, wishartCalls)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != "ClassyFire rate limited (HTTP 429)" {
		t.Errorf("expected rate-limit error surfaced after retries, got %+v", cf)
	}
}

// cfb failing cfbDownGiveUp times in a row is skipped, later keys go straight to Wishart
func TestEnrichWithClassyFireSkipsCfbWhenDown(t *testing.T) {
	var cfbCalls, wishartCalls int32
	mockRoutes(t, counted(&cfbCalls, unreachable), counted(&wishartCalls, classified))
	cfbRouteDownFor = time.Hour

	results := singleKeyResults("AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N",
		"CYYVLZVUVIJVGH-UHFFFAOYSA-N", "DYYVLZVUVIJVGH-UHFFFAOYSA-N", "EYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if cfbCalls != int32(cfbDownGiveUp) || wishartCalls != 5 {
		t.Errorf("expected %d cfb and 5 Wishart calls, got %d and %d", cfbDownGiveUp, cfbCalls, wishartCalls)
	}
	for i, r := range results {
		if cf := r.Matches[0].ClassyFire; cf == nil || cf.Error != "" {
			t.Errorf("result[%d]: expected classification, got %+v", i, cf)
		}
	}
}

// With Wishart down, a cfb 429 is retried on cfb rather than counted as ClassyFire being down
func TestEnrichWithClassyFireCfbRateLimitWithWishartDownRetries(t *testing.T) {
	var cfbCalls int32
	mockRoutes(t, func(key string) (cfbFetch, error) {
		if atomic.AddInt32(&cfbCalls, 1) < cfb429MaxRetries {
			return rateLimited(key)
		}
		return classified(key)
	}, unreachable)

	results := singleKeyResults("RYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != "" {
		t.Errorf("expected classification once cfb answered, got %+v", cf)
	}
}

// With both routes skipped as down, compounds fail fast without any request
func TestEnrichWithClassyFireBothRoutesDownFailsFast(t *testing.T) {
	var calls int32
	mockClassyFire(t, counted(&calls, classified))
	cfbGateMu.Lock()
	cfbSkipUntil, wishartSkipUntil = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
	cfbGateMu.Unlock()

	results := singleKeyResults("AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if calls != 0 {
		t.Errorf("expected no requests, got %d", calls)
	}
	for i, r := range results {
		if cf := r.Matches[0].ClassyFire; cf == nil || cf.Error != cfbUnavailableNote {
			t.Errorf("result[%d]: expected %q, got %+v", i, cfbUnavailableNote, cf)
		}
	}
}

func TestCfbStandardKey(t *testing.T) {
	if got, ok := cfbStandardKey("QNAYBMKLOCPYGJ-REOHCLBHSA-N"); !ok || got != "QNAYBMKLOCPYGJ-UHFFFAOYSA-N" {
		t.Errorf("expected the standard block swapped in, got %q (ok=%v)", got, ok)
	}
	if _, ok := cfbStandardKey("RYYVLZVUVIJVGH-UHFFFAOYSA-N"); ok {
		t.Error("a key that already has the standard block must not be retried")
	}
}

// A 404 from Wishart directly retries the standard-block key, as cfb does, and caches it under the original key
func TestEnrichWithClassyFireDirectNotFoundTriesStandardKey(t *testing.T) {
	const key, std = "QNAYBMKLOCPYGJ-REOHCLBHSA-N", "QNAYBMKLOCPYGJ-UHFFFAOYSA-N"
	freshKey(t, key)
	freshKey(t, std)
	var wishartCalls int32
	mockRoutes(t, rateLimited, counted(&wishartCalls, func(k string) (cfbFetch, error) {
		if k == std {
			return classified(k)
		}
		return cfbFetch{info: &model.ClassyFireInfo{Error: cfbNotFoundNote}}, nil
	}))

	results := singleKeyResults(key)
	enrichWithClassyFire(context.Background(), results)

	if wishartCalls != 2 {
		t.Errorf("expected Wishart asked for the key and the standard key, got %d calls", wishartCalls)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != "" {
		t.Errorf("expected the standard key's classification, got %+v", cf)
	}
	if cached, ok := cfbCacheLookup(key); !ok || cached.Error != "" {
		t.Errorf("expected the classification cached under the original key, got %+v (ok=%v)", cached, ok)
	}
}

// A standard key that 404s from Wishart directly is final, there is nothing to fall back to
func TestEnrichWithClassyFireDirectNotFoundStandardKeyIsFinal(t *testing.T) {
	var wishartCalls int32
	mockRoutes(t, rateLimited, counted(&wishartCalls, func(string) (cfbFetch, error) {
		return cfbFetch{info: &model.ClassyFireInfo{Error: cfbNotFoundNote}}, nil
	}))

	results := singleKeyResults("RYYVLZVUVIJVGH-UHFFFAOYSA-N")
	enrichWithClassyFire(context.Background(), results)

	if wishartCalls != 1 {
		t.Errorf("expected a single Wishart call, got %d", wishartCalls)
	}
	if cf := results[0].Matches[0].ClassyFire; cf == nil || cf.Error != cfbNotFoundNote {
		t.Errorf("expected %q, got %+v", cfbNotFoundNote, cf)
	}
}

// Misses wait for the next slot once the allowance is spent, cache hits hand their slot back
func TestEnrichWithClassyFireSlotsPaceMissesNotHits(t *testing.T) {
	origCap := cfbSlotCap
	t.Cleanup(func() { cfbSlotCap = origCap })

	run := func(hit bool) time.Duration {
		mockClassyFire(t, func(string) (cfbFetch, error) {
			return cfbFetch{info: fakeClassyFireInfo(), cacheHit: hit}, nil
		})
		cfbSlotCap, cfbSlotRefill = 1, 80*time.Millisecond
		start := time.Now()
		enrichWithClassyFire(context.Background(), singleKeyResults("AAAAAAAAAAAAAA-BBBBBBBBBB-A", "CCCCCCCCCCCCCC-DDDDDDDDDD-B"))
		return time.Since(start)
	}

	if elapsed := run(false); elapsed < 80*time.Millisecond {
		t.Errorf("expected the second miss to wait for a refill (80ms), took %s", elapsed)
	}
	if elapsed := run(true); elapsed >= 80*time.Millisecond {
		t.Errorf("expected cache hits not to wait for a refill, took %s", elapsed)
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
	streamClassyFire(ctx, cfbKeyChan([]string{"A", "B", "C"}), func(key string, info *model.ClassyFireInfo) {
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
	classyFireFetcher = func(context.Context, string) (cfbFetch, error) {
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
	go func() { defer wg.Done(); streamClassyFire(context.Background(), cfbKeyChan(keys), record("A")) }()
	go func() { defer wg.Done(); streamClassyFire(context.Background(), cfbKeyChan(keys), record("B")) }()
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

// An HTTP 500 is flagged with the internal-error message and the probe-aware mode
func TestDefaultFetcherInternalServerError(t *testing.T) {
	key := freshKey(t, "INTERNALERR001-AAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusInternalServerError, "", nil), nil
	})

	res, err := defaultClassyFireFetcher(context.Background(), key)
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
	if res.mode != cfbRetry500 {
		t.Errorf("mode: want cfbRetry500, got %v", res.mode)
	}
	if res.errMsg != "ClassyFire internal error (HTTP 500)" {
		t.Errorf("errMsg: want %q, got %q", "ClassyFire internal error (HTTP 500)", res.errMsg)
	}
	// A 500 is transient and must not be cached
	if _, ok := cfbCacheLookup(key); ok {
		t.Error("500 must not be cached")
	}
}

// An unparseable 200 body is treated as a transient failure
func TestDefaultFetcherMalformedJSON(t *testing.T) {
	key := freshKey(t, "MALFORMED00001-AAAAAAAAA-N")
	withMockTransport(t, func(*http.Request) (*http.Response, error) {
		return cfbResp(http.StatusOK, "this is not json", nil), nil
	})

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

	res, err := defaultClassyFireFetcher(context.Background(), key)
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

// setCfbServiceUp sets the reachability flag and restores it after the test
func setCfbServiceUp(t *testing.T, up bool) {
	t.Helper()
	orig := cfbServiceUp.Load()
	cfbServiceUp.Store(up)
	t.Cleanup(func() { cfbServiceUp.Store(orig) })
}

func TestClassyFireStatusReportsFlag(t *testing.T) {
	for _, up := range []bool{true, false} {
		setCfbServiceUp(t, up)
		rec := httptest.NewRecorder()
		ClassyFireStatus(rec, httptest.NewRequest(http.MethodGet, "/classyfire/status", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want 200", rec.Code)
		}
		var body struct {
			Up bool `json:"up"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if body.Up != up {
			t.Errorf("up = %v, want %v", body.Up, up)
		}
	}
}

// StartClassyFireHealthCheck runs the probe immediately, so a stubbed-down probe
// should flip the flag to down shortly after launch
func TestStartClassyFireHealthCheckUpdatesFlag(t *testing.T) {
	setCfbServiceUp(t, true)
	origProbe := cfbHealthProbe
	cfbHealthProbe = func() bool { return false }
	t.Cleanup(func() { cfbHealthProbe = origProbe })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartClassyFireHealthCheck(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !cfbServiceUp.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cfbServiceUp not set to false after health check launch")
}
