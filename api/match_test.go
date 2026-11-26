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

func TestMatchCAS(t *testing.T) {
	var hit model.SingleResult
	matchCAS(mockIndex, "7732-18-5", &hit, false)
	// Other tests reload the CSV into the shared memory DB, so only the first match is checked
	if !hit.MatchFound || hit.MatchLevel != "Exact CAS" || len(hit.Matches) == 0 {
		t.Fatalf("expected an Exact CAS match, got %+v", hit)
	}
	if hit.Matches[0].Identifier != "1" {
		t.Errorf("expected water (CID 1), got CID %s", hit.Matches[0].Identifier)
	}

	// 64-17-5 is a valid CAS ID that the test data does not hold
	var miss model.SingleResult
	matchCAS(mockIndex, "64-17-5", &miss, false)
	if miss.MatchFound || miss.ErrMsg != "No compound found" {
		t.Errorf("expected a miss, got %+v", miss)
	}
}

func TestMatchHMDB(t *testing.T) {
	cases := []struct {
		query, wantID, wantConverted string
	}{
		{"HMDB0002111", "1", ""},
		// The CSV stores methane as HMDB02714, both sides pad to 7 digits
		{"hmdb02714", "2", "HMDB0002714"},
		{"HMDB:2714", "2", "HMDB0002714"},
	}
	for _, tc := range cases {
		var res model.SingleResult
		matchHMDB(mockIndex, tc.query, &res, false)
		if !res.MatchFound || res.MatchLevel != "Exact HMDB" || len(res.Matches) == 0 {
			t.Fatalf("%s: expected an Exact HMDB match, got %+v", tc.query, res)
		}
		if res.Matches[0].Identifier != tc.wantID {
			t.Errorf("%s: expected CID %s, got %s", tc.query, tc.wantID, res.Matches[0].Identifier)
		}
		if res.ConvertedQuery != tc.wantConverted {
			t.Errorf("%s: expected converted_query %q, got %q", tc.query, tc.wantConverted, res.ConvertedQuery)
		}
	}

	// A miss still shows the padded ID it looked up
	var miss model.SingleResult
	matchHMDB(mockIndex, "HMDB1", &miss, false)
	if miss.MatchFound || miss.ErrMsg != "No compound found" || miss.ConvertedQuery != "HMDB0000001" {
		t.Errorf("expected a miss read as HMDB0000001, got %+v", miss)
	}
}

// Lookups that have no query shape reaching them through Match on a closed DB
func TestMatchDirectErrorPaths(t *testing.T) {
	closed, err := model.LoadCSVToMemory("../dataset/test_datasets/unittest_data.csv")
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}
	closed.Close()

	for name, run := range map[string]func(*model.SingleResult){
		"cas":               func(r *model.SingleResult) { matchCAS(closed, "7732-18-5", r, false) },
		"hmdb":              func(r *model.SingleResult) { matchHMDB(closed, "HMDB0002111", r, false) },
		"smiles_or_formula": func(r *model.SingleResult) { matchSmilesOrFormula(closed, "CO", r, true, false) },
	} {
		var res model.SingleResult
		run(&res)
		if res.MatchFound || res.ErrMsg != "Internal server error" {
			t.Errorf("%s: expected an internal error, got %+v", name, res)
		}
	}

	// A name error leaves the result as it was, so the earlier error survives
	res := model.SingleResult{ErrMsg: "No compound found"}
	matchName(closed, "water", &res, false)
	if res.MatchFound || res.ErrMsg != "No compound found" {
		t.Errorf("expected the result untouched, got %+v", res)
	}
}

func TestMatchSmilesMisses(t *testing.T) {
	t.Run("too long for RDKit", func(t *testing.T) {
		mockSmilesConverter(t, func(string) (string, error) {
			t.Fatal("a SMILES over 4096 characters must not reach RDKit")
			return "", nil
		})
		var res model.SingleResult
		matchSmiles(mockIndex, strings.Repeat("C", 4097), &res, true, false)
		if res.MatchFound || res.ErrMsg != "No compound found" {
			t.Errorf("expected a miss, got %+v", res)
		}
	})

	t.Run("RDKit cannot read it", func(t *testing.T) {
		mockSmilesConverter(t, func(string) (string, error) { return "", io.EOF })
		var res model.SingleResult
		matchSmiles(mockIndex, "C1CC", &res, true, false)
		if res.MatchFound || res.ErrMsg != "RDKit conversion failed" || res.DerivedInChIKey != "" {
			t.Errorf("expected an RDKit failure, got %+v", res)
		}
	})

	t.Run("first block disabled", func(t *testing.T) {
		var res model.SingleResult
		matchInchiKey(mockIndex, "MYFAKEINCHIKEY-NOTNOTNOTN-O", &res, false, false)
		if res.MatchFound || res.ErrMsg != "No compound found, first block matches disabled" {
			t.Errorf("expected first block to be skipped, got %+v", res)
		}
	})
}

func TestSmilesOrFormulaFallsBackToFormula(t *testing.T) {
	mockSmilesConverter(t, func(string) (string, error) { return "", io.EOF })

	// The SMILES side fails, so the formula lookup decides and its error replaces RDKit's
	var hit model.SingleResult
	matchSmilesOrFormula(mockIndex, "CH4", &hit, true, false)
	if !hit.MatchFound || hit.QueryType != "formula" || hit.MatchLevel != "Exact Formula" || hit.ErrMsg != "" {
		t.Fatalf("expected a formula match, got %+v", hit)
	}
	if hit.Matches[0].Identifier != "2" {
		t.Errorf("expected methane (CID 2), got CID %s", hit.Matches[0].Identifier)
	}

	var miss model.SingleResult
	matchSmilesOrFormula(mockIndex, "C9", &miss, true, false)
	if miss.MatchFound || miss.ErrMsg != "No compound found" {
		t.Errorf("expected a formula miss, got %+v", miss)
	}
}

func TestMatchNameVariant(t *testing.T) {
	// "isomer a" is stripped by synonyms.Variants, so the lookup lands on "methane"
	var res model.SingleResult
	matchName(mockIndex, "Methane isomer A", &res, false)
	if !res.MatchFound || res.MatchLevel != "Name Variant" || res.ConvertedQuery != "methane" || res.QueryType != "name" {
		t.Fatalf("expected a name variant match, got %+v", res)
	}
	if res.Matches[0].Identifier != "2" {
		t.Errorf("expected methane (CID 2), got CID %s", res.Matches[0].Identifier)
	}
}

func TestMatchForcedRunsOnlyThatLookup(t *testing.T) {
	cases := []struct {
		forced, detected, query, wantLevel string
	}{
		{"pubchem_id", "pubchem_id", "1", "Exact PubChem ID"},
		{"inchikey", "inchikey", "MYFAKEINCHIKEY-ISRIGHTHER-E", "Exact InChIKey"},
		{"inchi", "inchi", "InChI=1S/H2O/h1H2", "Exact InChI"},
		{"hmdb", "hmdb", "HMDB0002111", "Exact HMDB"},
	}
	for _, tc := range cases {
		res := model.SingleResult{QueryType: tc.detected}
		matchForced(mockIndex, tc.query, &res, tc.forced, true, false)
		if !res.MatchFound || res.MatchLevel != tc.wantLevel || res.QueryType != tc.forced {
			t.Errorf("%s: expected %s, got %+v", tc.forced, tc.wantLevel, res)
		}
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
