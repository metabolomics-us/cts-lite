package model

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const testCSV = "../dataset/test_datasets/unittest_data.csv"

// loadTestIndex is a helper that loads the shared test CSV into memory.
func loadTestIndex(t *testing.T) *PubChemIndex {
	t.Helper()
	idx, err := LoadCSVToMemory(testCSV)
	if err != nil {
		t.Fatalf("LoadCSVToMemory failed: %v", err)
	}
	return idx
}

func TestLoadCSVToMemory(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	// Sanity-check: at least one compound is queryable
	compounds, err := idx.QueryBySmiles("O")
	if err != nil {
		t.Fatalf("QueryBySmiles returned error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}
}

func TestLoadCSVToMemory_FileNotFound(t *testing.T) {
	_, err := LoadCSVToMemory("/nonexistent/path/to.csv")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestQueryByInChIKey_Hit(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByInChIKey("MYFAKEINCHIKEY-ISRIGHTHER-E")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}
	if compounds[0].CompoundName != "Water" {
		t.Errorf("expected Water, got %s", compounds[0].CompoundName)
	}
}

func TestQueryByInChIKey_Miss(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByInChIKey("ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 0 {
		t.Errorf("expected 0 compounds, got %d", len(compounds))
	}
}

// TestQueryByFirstBlock_OrderedByScore verifies that multiple compounds sharing
// a first block are returned in descending SortingScore order (0.7*pubmed + 0.3*patent).
// Water: 0.7*10 + 0.3*2 = 7.6   Methane: 0.7*18 + 0.3*7 = 14.7  → Methane first.
func TestQueryByFirstBlock_OrderedByScore(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByFirstBlock("MYFAKEINCHIKEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 2 {
		t.Fatalf("expected 2 compounds, got %d", len(compounds))
	}

	want := []string{"Methane", "Water"}
	got := []string{compounds[0].CompoundName, compounds[1].CompoundName}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ordering mismatch (-want +got):\n%s", diff)
	}
}

func TestQueryByFirstBlock_Miss(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByFirstBlock("DOESNOTEXIST00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 0 {
		t.Errorf("expected 0 compounds, got %d", len(compounds))
	}
}

func TestQueryByInChI_Hit(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByInChI("InChI=1S/CH4/h1H4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}
	if compounds[0].CompoundName != "Methane" {
		t.Errorf("expected Methane, got %s", compounds[0].CompoundName)
	}
}

func TestQueryByInChI_Miss(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByInChI("InChI=1S/NOTHING")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 0 {
		t.Errorf("expected 0 compounds, got %d", len(compounds))
	}
}

func TestQueryBySmiles_Hit(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryBySmiles("C=O")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}
	if compounds[0].CompoundName != "Formaldehyde" {
		t.Errorf("expected Formaldehyde, got %s", compounds[0].CompoundName)
	}
}

func TestQueryBySmiles_Miss(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryBySmiles("CC(O)=O")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 0 {
		t.Errorf("expected 0 compounds, got %d", len(compounds))
	}
}

func TestQueryByFormula_Hit(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByFormula("CH2O")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}
	if compounds[0].CompoundName != "Formaldehyde" {
		t.Errorf("expected Formaldehyde, got %s", compounds[0].CompoundName)
	}
}

func TestQueryByFormula_Miss(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByFormula("C99H99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 0 {
		t.Errorf("expected 0 compounds, got %d", len(compounds))
	}
}

// TestQuery_ClosedDB verifies that all QueryBy* methods surface an error
// (rather than panic) when the underlying database has been closed.
func TestQuery_ClosedDB(t *testing.T) {
	idx := loadTestIndex(t)
	idx.Close() // intentionally close before querying

	queries := []struct {
		name string
		fn   func() ([]*Compound, error)
	}{
		{"QueryByInChIKey", func() ([]*Compound, error) { return idx.QueryByInChIKey("MYFAKEINCHIKEY-ISRIGHTHER-E") }},
		{"QueryByFirstBlock", func() ([]*Compound, error) { return idx.QueryByFirstBlock("MYFAKEINCHIKEY") }},
		{"QueryByInChI", func() ([]*Compound, error) { return idx.QueryByInChI("InChI=1S/H2O/h1H2") }},
		{"QueryBySmiles", func() ([]*Compound, error) { return idx.QueryBySmiles("O") }},
		{"QueryByFormula", func() ([]*Compound, error) { return idx.QueryByFormula("H2O") }},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			_, err := q.fn()
			if err == nil {
				t.Errorf("%s: expected error on closed DB, got nil", q.name)
			}
		})
	}
}

// TestCompoundFields verifies that all Compound fields are populated correctly
// after a round-trip through LoadCSVToMemory.
func TestCompoundFields(t *testing.T) {
	idx := loadTestIndex(t)
	defer idx.Close()

	compounds, err := idx.QueryByInChIKey("MYFAKEINCHIKEY-ISRIGHTHER-E")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(compounds) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(compounds))
	}

	want := &Compound{
		Identifier:       "1",
		InChIKey:         "MYFAKEINCHIKEY-ISRIGHTHER-E",
		InChI:            "InChI=1S/H2O/h1H2",
		Smiles:           "O",
		CompoundName:     "Water",
		MolecularFormula: "H2O",
		MonoisotopicMass: 100,
		PubMedCount:      10,
		PatentCount:      2,
	}
	if diff := cmp.Diff(want, compounds[0]); diff != "" {
		t.Errorf("compound fields mismatch (-want +got):\n%s", diff)
	}
}
