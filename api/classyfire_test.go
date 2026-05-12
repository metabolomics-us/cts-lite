package api

import (
	"ctslite/model"
	"errors"
	"testing"
)

// mockClassyFire replaces classyFireFetcher for the duration of a test.
// Returns the original fetcher so callers can defer its restoration.
func mockClassyFire(t *testing.T, fn func(string) (*model.ClassyFireInfo, error)) {
	t.Helper()
	orig := classyFireFetcher
	classyFireFetcher = fn
	t.Cleanup(func() { classyFireFetcher = orig })
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
	mockClassyFire(t, func(inchikey string) (*model.ClassyFireInfo, error) {
		return fakeClassyFireInfo(), nil
	})

	results := []*model.SingleResult{
		{
			MatchFound: true,
			Matches:    []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}},
		},
	}
	enrichWithClassyFire(results)

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

func TestEnrichWithClassyFireDeduplicatesInChIKeys(t *testing.T) {
	// Two results share the same InChIKey — fetcher must be called only once.
	callCount := 0
	mockClassyFire(t, func(inchikey string) (*model.ClassyFireInfo, error) {
		callCount++
		return fakeClassyFireInfo(), nil
	})

	sharedKey := "RYYVLZVUVIJVGH-UHFFFAOYSA-N"
	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: sharedKey}}},
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: sharedKey}}},
	}
	enrichWithClassyFire(results)

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

func TestEnrichWithClassyFireSkipsNoMatches(t *testing.T) {
	called := false
	mockClassyFire(t, func(inchikey string) (*model.ClassyFireInfo, error) {
		called = true
		return fakeClassyFireInfo(), nil
	})

	results := []*model.SingleResult{
		{MatchFound: false, Matches: nil},
	}
	enrichWithClassyFire(results)

	if called {
		t.Error("expected ClassyFire fetcher not to be called when there are no matches")
	}
}

func TestEnrichWithClassyFireHandlesNotFound(t *testing.T) {
	// Fetcher returns nil, nil — compound not in ClassyFire database.
	mockClassyFire(t, func(inchikey string) (*model.ClassyFireInfo, error) {
		return nil, nil
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "AAAAAAAAAAAAAA-AAAAAAAAAA-A"}}},
	}
	enrichWithClassyFire(results)

	// ClassyFire field should remain nil — not an error, just not classified.
	if results[0].Matches[0].ClassyFire != nil {
		t.Error("expected nil ClassyFire for unclassified compound")
	}
}

func TestEnrichWithClassyFireHandlesFetchError(t *testing.T) {
	// Fetcher returns an error — result should still be returned without ClassyFire.
	mockClassyFire(t, func(inchikey string) (*model.ClassyFireInfo, error) {
		return nil, errors.New("connection refused")
	})

	results := []*model.SingleResult{
		{MatchFound: true, Matches: []*model.Compound{{InChIKey: "RYYVLZVUVIJVGH-UHFFFAOYSA-N"}}},
	}
	enrichWithClassyFire(results) // must not panic

	if results[0].Matches[0].ClassyFire != nil {
		t.Error("expected nil ClassyFire after fetch error")
	}
}
