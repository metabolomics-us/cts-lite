package api

import (
	"ctslite/model"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var mockIndex *model.PubChemIndex

// pcbNetworkDisabledTransport fails every request instantly. It is the
// test-binary default for pcbHTTPClient
type pcbNetworkDisabledTransport struct{}

func (pcbNetworkDisabledTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access disabled in tests; use withMockPCBTransport")
}

func TestMain(m *testing.M) {
	var err error
	mockIndex, err = model.LoadCSVToMemory("../dataset/test_datasets/unittest_data.csv")
	if err != nil {
		log.Fatalf("failed to load test CSV: %v", err)
	}

	// Default PubChem fallback to fail fast and offline unless a test
	// explicitly mocks it; also zero the pacing/retry delays for testing
	pcbHTTPClient = &http.Client{Transport: pcbNetworkDisabledTransport{}}
	pcbMinGap, pcbThrottledGap, pcbThrottlePause, pcbIdleReset = 0, 0, 0, 0
	pcbRetryDelays = []time.Duration{0, 0}

	os.Exit(m.Run())
}

// Data must match unittest_data.csv exactly
func fakeWaterCompound() *model.Compound {
	return &model.Compound{
		Identifier:          "1",
		InChIKey:            "MYFAKEINCHIKEY-ISRIGHTHER-E",
		InChI:               "InChI=1S/H2O/h1H2",
		Smiles:              "O",
		CompoundName:        "Water",
		MolecularFormula:    "H2O",
		ExactMass:           100,
		LiteratureCount:     10,
		PatentCount:         2,
		AnnotationTypeCount: 6,
		CASIDs:              []string{"7732-18-5"},
		HMDBIDs:             []string{"HMDB0002111", "HMDB0062587"},
	}
}

// Data must match unittest_data.csv exactly
func fakeFormaldehyde() *model.Compound {
	return &model.Compound{
		Identifier:          "3",
		InChIKey:            "FAKEFORMALDEHY-FAKEFRMALD-E",
		InChI:               "InChI=1S/CH2O/c1-2/h1H2",
		Smiles:              "C=O",
		CompoundName:        "Formaldehyde",
		MolecularFormula:    "CH2O",
		ExactMass:           30,
		LiteratureCount:     5,
		PatentCount:         1,
		AnnotationTypeCount: 4,
		CASIDs:              []string{"50-00-0"},
	}
}

// Data must match unittest_data.csv exactly
func fakeMethaneCompound() *model.Compound {
	return &model.Compound{
		Identifier:          "2",
		InChIKey:            "MYFAKEINCHIKEY-ANOTHERONE-E",
		InChI:               "InChI=1S/CH4/h1H4",
		Smiles:              "C",
		CompoundName:        "Methane",
		MolecularFormula:    "CH4",
		ExactMass:           99,
		LiteratureCount:     18,
		PatentCount:         7,
		AnnotationTypeCount: 8,
		CASIDs:  []string{"74-82-8", "1333-74-0"},
		HMDBIDs: []string{"HMDB0002714"},
	}
}

// Compares compound from response with expected compound
func assertCompound(t *testing.T, want *model.Compound, got *model.Compound) {
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("compound mismatch (-want +got):\n%s", diff)
	}
}

// Performs a match request, boiler plate method to avoid duplicate code
func doMatchRequest(t *testing.T, payload string, extraHeaders map[string]string, allHits bool) *http.Response {
	t.Helper()
	url := "/match"
	if allHits {
		url = "/match?top_hit_only=false"
	}
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)
	return w.Result()
}

func parseMatchResults(t *testing.T, res *http.Response) []*model.SingleResult {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 but got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	if err := json.Unmarshal(body, &results); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	return results
}

func mockSmilesConverter(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := smilesToInChIKey
	smilesToInChIKey = fn
	t.Cleanup(func() { smilesToInChIKey = orig })
}

func mockInChIConverter(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := inchiToInChIKey
	inchiToInChIKey = fn
	t.Cleanup(func() { inchiToInChIKey = orig })
}
