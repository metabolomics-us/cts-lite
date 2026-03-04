package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var mockIndex *model.PubChemIndex

func TestMain(m *testing.M) {
	var err error
	mockIndex, err = model.LoadPubChemLite("../../data/test_datasets/unittest_pubchemlite.csv")
	if err != nil {
		log.Fatalf("failed to load test CSV: %v", err)
	}
	os.Exit(m.Run())
}

// Data must match mock_pubchemlite.csv exactly
func fakeWaterCompound() *model.Compound {
	return &model.Compound{
		Identifier:       "1",
		InChIKey:         "MYFAKEINCHIKEY-ISRIGHTHER-E",
		FirstBlock:       "MYFAKEINCHIKEY",
		InChI:            "InChI=1S/H2O/h1H2",
		Smiles:           "O",
		CompoundName:     "Water",
		MolecularFormula: "H2O",
		MonoisotopicMass: "100",
		PubMedCount:      "10",
		PatentCount:      "2",
	}
}

// Data must match mock_pubchemlite.csv exactly
func fakeMethaneCompound() *model.Compound {
	return &model.Compound{
		Identifier:       "2",
		InChIKey:         "MYFAKEINCHIKEY-ANOTHERONE-E",
		FirstBlock:       "MYFAKEINCHIKEY",
		InChI:            "InChI=1S/CH4/h1H4",
		Smiles:           "C",
		CompoundName:     "Methane",
		MolecularFormula: "CH4",
		MonoisotopicMass: "99",
		PubMedCount:      "18",
		PatentCount:      "7",
	}
}

// Compares compound from response with expected compound
func assertCompound(t *testing.T, want *model.Compound, got *model.Compound) {
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("compound mismatch (-want +got):\n%s", diff)
	}
}

func doMatchRequest(t *testing.T, payload string, extraHeaders map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
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

func TestStatusHealthEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	Status(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", w.Result().StatusCode)
	}
}

func TestDeprecatedGetRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/match?q=O", nil)
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	results := parseMatchResults(t, w.Result())

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}
	assertCompound(t, fakeWaterCompound(), results[0].Matches[0])
}

func TestMatchEndpoints(t *testing.T) {
	water := fakeWaterCompound()
	methane := fakeMethaneCompound()

	tests := []struct {
		name        string
		query       string
		wantMatches []*model.Compound // ordered as expected in results[0].Matches
	}{
		{
			name:        "smiles",
			query:       "O",
			wantMatches: []*model.Compound{water},
		},
		{
			name:        "full InChIKey",
			query:       "MYFAKEINCHIKEY-ANOTHERONE-E",
			wantMatches: []*model.Compound{methane},
		},
		{
			name:        "first block (returns both compounds, methane first by SortingScore)",
			query:       "MYFAKEINCHIKEY-NOTNOTNOTN-O",
			wantMatches: []*model.Compound{methane, water},
		},
		{
			name:        "InChI",
			query:       "InChI=1S/H2O/h1H2",
			wantMatches: []*model.Compound{water},
		},
		{
			name:        "molecular formula",
			query:       "CH4",
			wantMatches: []*model.Compound{methane},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := doMatchRequest(t, `{"queries":"`+tc.query+`"}`, nil)
			results := parseMatchResults(t, res)

			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if len(results[0].Matches) != len(tc.wantMatches) {
				t.Fatalf("expected %d compound(s), got %d", len(tc.wantMatches), len(results[0].Matches))
			}
			for i, want := range tc.wantMatches {
				assertCompound(t, want, results[0].Matches[i])
			}
		})
	}
}

func TestMultiQuery(t *testing.T) {
	// 5 queries: smiles O, smiles C, bad smiles, fake inchikey, bad InChI // space separated (%20)
	res := doMatchRequest(t, `{"queries":"O C BADSMILES MYFAKEINCHIKEY-ISRIGHTHER-E InChI=BADINCHI"}`, nil)
	results := parseMatchResults(t, res)

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// Just confirm that the first two did in fact get the right matches
	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}
	if len(results[1].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[1].Matches))
	}

	assertCompound(t, fakeWaterCompound(), results[0].Matches[0])
	assertCompound(t, fakeMethaneCompound(), results[1].Matches[0])
}

func TestCSVFormatResponse(t *testing.T) {
	res := doMatchRequest(t, `{"queries":"O"}`, map[string]string{"Accept": "text/csv"})

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	csvReader := csv.NewReader(res.Body)
	records, err := csvReader.ReadAll()
	if err != nil {
		t.Fatalf("failed to read CSV response: %v", err)
	}

	// Expecting header + 1 data row
	if len(records) != 2 {
		t.Fatalf("expected 2 CSV rows, got %d", len(records))
	}

	// Check header
	expectedHeader := []string{
		"query", "query_type", "found_match", "match_level", "error_message",
		"inchikey", "first_block", "inchi", "smiles", "compound_name",
		"molecular_formula", "pubmed_count", "patent_count",
	}
	if diff := cmp.Diff(expectedHeader, records[0]); diff != "" {
		t.Errorf("CSV header mismatch (-want +got):\n%s", diff)
	}

	// Check data row
	expectedData := []string{
		"O", "smiles", "true", "Exact SMILES", "",
		"MYFAKEINCHIKEY-ISRIGHTHER-E", "MYFAKEINCHIKEY", "InChI=1S/H2O/h1H2", "O", "Water", "H2O", "10", "2",
	}
	if diff := cmp.Diff(expectedData, records[1]); diff != "" {
		t.Errorf("CSV data row mismatch (-want +got):\n%s", diff)
	}
}
