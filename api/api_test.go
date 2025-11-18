package api

import (
	"ctslite/model"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		InChIKey:         "MYFAKEINCHIKEY-ISRIGHTHER-E",
		FirstBlock:       "MYFAKEINCHIKEY",
		InChI:            "InChI=1S/H2O/h1H2",
		Smiles:           "O",
		CompoundName:     "Water",
		MolecularFormula: "H2O",
		PubMedCount:      "10",
		PatentCount:      "2",
	}
}

// Data must match mock_pubchemlite.csv exactly
func fakeMethaneCompound() *model.Compound {
	return &model.Compound{
		InChIKey:         "MYFAKEINCHIKEY-ANOTHERONE-E",
		FirstBlock:       "MYFAKEINCHIKEY",
		InChI:            "InChI=1S/CH4/h1H4",
		Smiles:           "C",
		CompoundName:     "Methane",
		MolecularFormula: "CH4",
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

func TestSmilesMatchEndpoint(t *testing.T) {
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

func TestFullInChIKeyMatchEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/match?q=MYFAKEINCHIKEY-ANOTHERONE-E", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/match?q=MYFAKEINCHIKEY-NOTNOTNOTN-O", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/match?q=InChI=1S/H2O/h1H2", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/match?q=O%20C%20BADSMILES%20MYFAKEINCHIKEY-ISRIGHTHER-E%20InChI=BADINCHI", nil)
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
