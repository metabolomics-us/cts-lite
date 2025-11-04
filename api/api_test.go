package api

import (
	"ctslite/data"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func loadMockIndex(t *testing.T) *data.PubChemIndex {
	index, err := data.LoadPubChemLite("testdata/mock_pubchemlite.csv")
	if err != nil {
		t.Fatalf("failed to load test CSV: %v", err)
	}
	return index
}

// Data must match mock_pubchemlite.csv exactly
func fakeWaterCompound() *data.Compound {
	return &data.Compound{
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
func fakeMethaneCompound() *data.Compound {
	return &data.Compound{
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
func assertCompound(t *testing.T, want *data.Compound, got *data.Compound) {
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
	var compounds []*data.Compound
	json.Unmarshal(body, &compounds)

	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}

	got := compounds[0]
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
	var compounds []*data.Compound
	json.Unmarshal(body, &compounds)

	if len(compounds) != 1 {
		t.Fatalf("Expected 1 compound, got %d", len(compounds))
	}

	got := compounds[0]
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
	var compounds []*data.Compound
	json.Unmarshal(body, &compounds)

	// First block matching should give us both our fake water and fake methane compounds
	if len(compounds) != 2 {
		t.Fatalf("Expected 2 compounds, got %d", len(compounds))
	}

	gotWater := compounds[0]
	gotMethane := compounds[1]

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
	var compounds []*data.Compound
	json.Unmarshal(body, &compounds)

	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}

	got := compounds[0]
	want := fakeWaterCompound()

	assertCompound(t, want, got)
}
