package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func loadMockIndex(t *testing.T) *model.PubChemIndex {
	index, err := model.LoadPubChemLite("../data/test_data/unittest_pubchemlite.csv")
	if err != nil {
		t.Fatalf("failed to load test CSV: %v", err)
	}
	return index
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

func TestStatusEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	Status(w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}
}

func TestDeprecatedGetRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/match?q=O", nil)

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}

	got := results[0].Matches[0]
	want := fakeWaterCompound()

	assertCompound(t, want, got)
}

func TestSmilesMatchEndpoint(t *testing.T) {
	payload := `{"queries":"O"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}

	got := results[0].Matches[0]
	want := fakeWaterCompound()

	assertCompound(t, want, got)
}

func TestFullInChIKeyMatchEndpoint(t *testing.T) {
	payload := `{"queries":"MYFAKEINCHIKEY-ANOTHERONE-E"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if len(results[0].Matches) != 1 {
		t.Fatalf("Expected 1 compound, got %d", len(results[0].Matches))
	}

	got := results[0].Matches[0]
	want := fakeMethaneCompound()

	assertCompound(t, want, got)
}

func TestFirstBlockMatchEndpoint(t *testing.T) {
	payload := `{"queries":"MYFAKEINCHIKEY-NOTNOTNOTN-O"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// First block matching should give us both our fake water and fake methane compounds
	if len(results[0].Matches) != 2 {
		t.Fatalf("Expected 2 compounds, got %d", len(results[0].Matches))
	}

	gotWater := results[0].Matches[0]
	gotMethane := results[0].Matches[1]

	wantWater := fakeWaterCompound()
	wantMethane := fakeMethaneCompound()

	assertCompound(t, wantWater, gotWater)
	assertCompound(t, wantMethane, gotMethane)
}

func TestInChIMatchEndpoint(t *testing.T) {
	payload := `{"queries":"InChI=1S/H2O/h1H2"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}

	got := results[0].Matches[0]
	want := fakeWaterCompound()

	assertCompound(t, want, got)
}

func TestMultiQuery(t *testing.T) {
	// 5 queries: smiles O, smiles C, bad smiles, fake inchikey, bad InChI // space separated (%20)
	payload := `{"queries":"O C BADSMILES MYFAKEINCHIKEY-ISRIGHTHER-E InChI=BADINCHI"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

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

	gotWater := results[0].Matches[0]
	wantWater := fakeWaterCompound()

	assertCompound(t, wantWater, gotWater)

	gotMethane := results[1].Matches[0]
	wantMethane := fakeMethaneCompound()

	assertCompound(t, wantMethane, gotMethane)
}

func TestFormulaMatchEndpoint(t *testing.T) {
	payload := `{"queries":"CH4"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 but got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	var results []*model.SingleResult
	json.Unmarshal(body, &results)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	gotMethane := results[0].Matches[0]
	wantMethane := fakeMethaneCompound()

	assertCompound(t, wantMethane, gotMethane)
}

func TestCSVFormatResponse(t *testing.T) {
	payload := `{"queries":"O"}`
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(payload))
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	mockIndex := loadMockIndex(t)
	Match(mockIndex, w, req)

	res := w.Result()

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
