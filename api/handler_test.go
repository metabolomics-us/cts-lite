package api

import (
	"ctslite/data"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func loadMockIndex(t *testing.T) *data.PubChemIndex {
	index, err := data.LoadPubChemLite("testdata/mock_pubchemlite.csv")
	if err != nil {
		t.Fatalf("failed to load test CSV: %v", err)
	}
	return index
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
	if got.CompoundName != "Water" {
		t.Errorf("expected CompoundName 'Water', got '%s'", got.CompoundName)
	}
	if got.MolecularFormula != "H2O" {
		t.Errorf("expected MolecularFormula 'H2O', got '%s'", got.MolecularFormula)
	}
}

func TestFullInChIKeyMatchEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/match?q=MYFAKEINCHIKEY-ISRIGHTHER-E", nil)
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
	if got.CompoundName != "Water" {
		t.Errorf("expected CompoundName 'Water', got '%s'", got.CompoundName)
	}
	if got.MolecularFormula != "H2O" {
		t.Errorf("expected MolecularFormula 'H2O', got '%s'", got.MolecularFormula)
	}
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

	if len(compounds) != 2 {
		t.Fatalf("expected 2 compounds, got %d", len(compounds))
	}

	got := compounds[0]
	if got.CompoundName != "Water" {
		t.Errorf("expected CompoundName 'Water', got '%s'", got.CompoundName)
	}
	if got.MolecularFormula != "H2O" {
		t.Errorf("expected MolecularFormula 'H2O', got '%s'", got.MolecularFormula)
	}
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
	if got.CompoundName != "Water" {
		t.Errorf("expected CompoundName 'Water', got '%s'", got.CompoundName)
	}
	if got.MolecularFormula != "H2O" {
		t.Errorf("expected MolecularFormula 'H2O', got '%s'", got.MolecularFormula)
	}
}
