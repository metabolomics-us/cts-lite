package api

import (
	"ctslite/model"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatchPubChemID(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		wantFound    bool
		wantErrMsg   string
		wantCompound *model.Compound
	}{
		{
			name:         "match by PubChem ID",
			query:        "1",
			wantFound:    true,
			wantCompound: fakeWaterCompound(),
		},
		{
			name:       "no match for unknown PubChem ID",
			query:      "999",
			wantFound:  false,
			wantErrMsg: "No local match; PubChem Live lookup unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := doMatchRequest(t, `{"queries":"`+tc.query+`"}`, nil, false)
			results := parseMatchResults(t, res)

			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].MatchFound != tc.wantFound {
				t.Errorf("expected MatchFound=%v, got %v", tc.wantFound, results[0].MatchFound)
			}
			if tc.wantFound {
				if len(results[0].Matches) != 1 {
					t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
				}
				assertCompound(t, tc.wantCompound, results[0].Matches[0])
			} else {
				if results[0].ErrMsg != tc.wantErrMsg {
					t.Errorf("expected error %q, got %q", tc.wantErrMsg, results[0].ErrMsg)
				}
			}
		})
	}
}

// brokenFirstBlockIndex loads a private in-memory index and drops the
// first_block column so that QueryByFirstBlock fails at execution time.
// A private ":memory:" DB is used so the schema change does not affect mockIndex.
func brokenFirstBlockIndex(t *testing.T) *model.PubChemIndex {
	t.Helper()
	idx, err := model.LoadCSVToPrivateMemory("../dataset/test_datasets/unittest_data.csv")
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}
	db := idx.DB()
	if _, err := db.Exec("DROP INDEX IF EXISTS idx_first_block"); err != nil {
		idx.Close()
		t.Fatalf("failed to drop first_block index: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE compounds DROP COLUMN first_block"); err != nil {
		idx.Close()
		t.Fatalf("failed to drop first_block column: %v", err)
	}
	return idx
}

// TestMatchInchIKeyFirstBlockErrorPath covers the second error branch in
// matchInchiKey: the exact InChIKey lookup succeeds (returns empty) but the
// subsequent first-block lookup fails because the column has been dropped.
func TestMatchInchIKeyFirstBlockErrorPath(t *testing.T) {
	// MYFAKEINCHIKEY-NOTNOTNOTN-O has no exact InChIKey match, so matchInchiKey
	// falls through to QueryByFirstBlock, which now returns an error.
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(`{"queries":"MYFAKEINCHIKEY-NOTNOTNOTN-O"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(brokenFirstBlockIndex(t), w, req)

	results := parseMatchResults(t, w.Result())
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MatchFound {
		t.Error("expected MatchFound=false on first-block DB error")
	}
	if results[0].ErrMsg != "Internal server error" {
		t.Errorf("expected 'Internal server error', got %q", results[0].ErrMsg)
	}
}

// TestMatchErrorPaths verifies that all match functions return an internal
// error message (rather than panicking) when the underlying database is closed.
// Each sub-test uses a fresh index so closing it does not affect other tests.
func TestMatchErrorPaths(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"inchi", `{"queries":"InChI=1S/H2O/h1H2"}`},
		{"inchikey_exact", `{"queries":"MYFAKEINCHIKEY-ISRIGHTHER-E"}`},
		// Full InChIKey miss -> falls through to first-block lookup
		{"inchikey_firstblock", `{"queries":"MYFAKEINCHIKEY-NOTNOTNOTN-O"}`},
		// C=O contains '=' so smilesGuaranteePattern matches -> type smiles -> matchSmiles
		{"smiles_direct", `{"queries":"C=O"}`},
		{"formula", `{"queries":"H2O"}`},
		{"formula_from_hydrogen_rule", `{"queries":"CH4"}`},
		// Numeric query -> type pubchem_id -> matchPubChemID
		{"pubchem_id", `{"queries":"1"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brokenIndex, err := model.LoadCSVToMemory("../dataset/test_datasets/unittest_data.csv")
			if err != nil {
				t.Fatalf("failed to load index: %v", err)
			}
			brokenIndex.Close() // close DB to force query errors

			req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			Match(brokenIndex, w, req)

			res := w.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", res.StatusCode)
			}
			body, _ := io.ReadAll(res.Body)
			var results []*model.SingleResult
			if err := json.Unmarshal(body, &results); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].MatchFound {
				t.Error("expected MatchFound=false on DB error")
			}
			if results[0].ErrMsg != "Internal server error" {
				t.Errorf("expected 'Internal server error', got %q", results[0].ErrMsg)
			}
		})
	}
}

func TestSmilesViaRDKitConversion(t *testing.T) {
	t.Run("exact InChIKey match", func(t *testing.T) {
		mockSmilesConverter(t, func(string) (string, error) {
			return "MYFAKEINCHIKEY-ISRIGHTHER-E", nil
		})
		res := doMatchRequest(t, `{"queries":"[C@@H](O)(N)C"}`, nil, false)
		results := parseMatchResults(t, res)

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if !results[0].MatchFound {
			t.Fatal("expected match via RDKit-converted InChIKey")
		}
		if results[0].QueryType != "converted_smiles" {
			t.Errorf("expected query_type 'converted_smiles', got %q", results[0].QueryType)
		}
		if results[0].MatchLevel != "Exact InChIKey" {
			t.Errorf("expected match_level 'Exact InChIKey', got %q", results[0].MatchLevel)
		}
		if results[0].ConvertedQuery != "MYFAKEINCHIKEY-ISRIGHTHER-E" {
			t.Errorf("expected converted_query 'MYFAKEINCHIKEY-ISRIGHTHER-E', got %q", results[0].ConvertedQuery)
		}
		assertCompound(t, fakeWaterCompound(), results[0].Matches[0])
	})

	t.Run("first block match", func(t *testing.T) {
		mockSmilesConverter(t, func(string) (string, error) {
			return "MYFAKEINCHIKEY-NOTNOTNOTN-O", nil
		})
		res := doMatchRequest(t, `{"queries":"[C@@H](O)(N)C"}`, nil, false)
		results := parseMatchResults(t, res)

		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if !results[0].MatchFound {
			t.Fatal("expected first block match via RDKit-converted InChIKey")
		}
		if results[0].QueryType != "converted_smiles" {
			t.Errorf("expected query_type 'converted_smiles', got %q", results[0].QueryType)
		}
		if results[0].MatchLevel != "First Block" {
			t.Errorf("expected match_level 'First Block', got %q", results[0].MatchLevel)
		}
		if results[0].ConvertedQuery != "MYFAKEINCHIKEY-NOTNOTNOTN-O" {
			t.Errorf("expected converted_query 'MYFAKEINCHIKEY-NOTNOTNOTN-O', got %q", results[0].ConvertedQuery)
		}
		assertCompound(t, fakeMethaneCompound(), results[0].Matches[0])
	})

	t.Run("no match when converter returns empty", func(t *testing.T) {
		mockSmilesConverter(t, func(string) (string, error) {
			return "", nil
		})
		res := doMatchRequest(t, `{"queries":"CC(O)=O"}`, nil, false)
		results := parseMatchResults(t, res)

		if results[0].MatchFound {
			t.Error("expected no match when converter returns empty")
		}
		// The PubChem fallback converts SMILES to an InChIKey before looking it up, so
		// a converter that yields nothing leaves it with nothing to query and
		// PubChem is never reached
		if results[0].ErrMsg != "No compound found (structure could not be read)" {
			t.Errorf("expected unreadable-structure message, got %q", results[0].ErrMsg)
		}
	})
}

// rdkit_conversion was removed, so a client still sending it must get the converted match
func TestRdkitConversionParamIgnored(t *testing.T) {
	mockSmilesConverter(t, func(string) (string, error) {
		return "MYFAKEINCHIKEY-ISRIGHTHER-E", nil
	})
	req := httptest.NewRequest(http.MethodPost, "/match?rdkit_conversion=false", strings.NewReader(`{"queries":"[C@@H](O)(N)C"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)
	results := parseMatchResults(t, w.Result())

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].MatchFound {
		t.Errorf("expected a match, got error %q", results[0].ErrMsg)
	}
	if results[0].QueryType != "converted_smiles" {
		t.Errorf("expected converted_smiles, got %q", results[0].QueryType)
	}
}

func TestSmilesOrFormulaKeepsConvertedSmilesType(t *testing.T) {
	// "CO" reads as both SMILES and formula, so it classifies as
	// smiles_or_formula. It misses the local SMILES lookup and is matched
	// through RDKit conversion, and matchSmilesOrFormula must keep the
	// converted_smiles query type rather than overwrite it with smiles
	mockSmilesConverter(t, func(string) (string, error) {
		return "MYFAKEINCHIKEY-ISRIGHTHER-E", nil
	})
	res := doMatchRequest(t, `{"queries":"CO"}`, nil, false)
	results := parseMatchResults(t, res)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].MatchFound {
		t.Fatal("expected smiles_or_formula match via converted SMILES InChIKey lookup")
	}
	if results[0].QueryType != "converted_smiles" {
		t.Errorf("expected query_type 'converted_smiles', got %q", results[0].QueryType)
	}
	if results[0].ConvertedQuery != "MYFAKEINCHIKEY-ISRIGHTHER-E" {
		t.Errorf("expected converted_query 'MYFAKEINCHIKEY-ISRIGHTHER-E', got %q", results[0].ConvertedQuery)
	}
	assertCompound(t, fakeWaterCompound(), results[0].Matches[0])
}
