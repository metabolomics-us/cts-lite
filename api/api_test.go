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
	mockIndex, err = model.LoadCSVToMemory("../dataset/test_datasets/unittest_data.csv")
	if err != nil {
		log.Fatalf("failed to load test CSV: %v", err)
	}
	os.Exit(m.Run())
}

// Data must match unittest_data.csv exactly
func fakeWaterCompound() *model.Compound {
	return &model.Compound{
		Identifier:       "1",
		InChIKey:         "MYFAKEINCHIKEY-ISRIGHTHER-E",
		InChI:            "InChI=1S/H2O/h1H2",
		Smiles:           "O",
		CompoundName:     "Water",
		MolecularFormula: "H2O",
		MonoisotopicMass: 100,
		LiteratureCount:  10,
		PatentCount:      2,
	}
}

// Data must match unittest_data.csv exactly
func fakeFormaldehyde() *model.Compound {
	return &model.Compound{
		Identifier:       "3",
		InChIKey:         "FAKEFORMALDEHY-FAKEFRMALD-E",
		InChI:            "InChI=1S/CH2O/c1-2/h1H2",
		Smiles:           "C=O",
		CompoundName:     "Formaldehyde",
		MolecularFormula: "CH2O",
		MonoisotopicMass: 30,
		LiteratureCount:  5,
		PatentCount:      1,
	}
}

// Data must match unittest_data.csv exactly
func fakeMethaneCompound() *model.Compound {
	return &model.Compound{
		Identifier:       "2",
		InChIKey:         "MYFAKEINCHIKEY-ANOTHERONE-E",
		InChI:            "InChI=1S/CH4/h1H4",
		Smiles:           "C",
		CompoundName:     "Methane",
		MolecularFormula: "CH4",
		MonoisotopicMass: 99,
		LiteratureCount:  18,
		PatentCount:      7,
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
		{
			name:        "direct smiles (smilesGuaranteePattern chars)",
			query:       "C=O",
			wantMatches: []*model.Compound{fakeFormaldehyde()},
		},
		{
			name:        "direct formula (formulaGuaranteePattern first char)",
			query:       "H2O",
			wantMatches: []*model.Compound{water},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := doMatchRequest(t, `{"queries":"`+tc.query+`"}`, nil, true)
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

func TestTopHitOnly(t *testing.T) {
	// MYFAKEINCHIKEY-NOTNOTNOTN-O matches via first block, returning both
	// Methane (score 14.7) and Water (score 7.6) when all hits are requested.
	const query = `{"queries":"MYFAKEINCHIKEY-NOTNOTNOTN-O"}`

	t.Run("default returns only top hit", func(t *testing.T) {
		res := doMatchRequest(t, query, nil, false)
		results := parseMatchResults(t, res)

		if len(results[0].Matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(results[0].Matches))
		}
		assertCompound(t, fakeMethaneCompound(), results[0].Matches[0])
	})

	t.Run("top_hit_only=true returns only top hit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/match?top_hit_only=true", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		Match(mockIndex, w, req)
		results := parseMatchResults(t, w.Result())

		if len(results[0].Matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(results[0].Matches))
		}
		assertCompound(t, fakeMethaneCompound(), results[0].Matches[0])
	})

	t.Run("top_hit_only=false returns all hits", func(t *testing.T) {
		res := doMatchRequest(t, query, nil, true)
		results := parseMatchResults(t, res)

		if len(results[0].Matches) != 2 {
			t.Fatalf("expected 2 matches, got %d", len(results[0].Matches))
		}
		assertCompound(t, fakeMethaneCompound(), results[0].Matches[0])
		assertCompound(t, fakeWaterCompound(), results[0].Matches[1])
	})
}

func TestMultiQuery(t *testing.T) {
	// 5 queries: smiles O, smiles C, bad smiles, fake inchikey, bad InChI // space separated (%20)
	res := doMatchRequest(t, `{"queries":"O C BADSMILES MYFAKEINCHIKEY-ISRIGHTHER-E InChI=BADINCHI"}`, nil, false)
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

func TestMatchErrors(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantErrMsg string
	}{
		{
			name:       "inchi no match",
			query:      "InChI=1S/NOTHING",
			wantErrMsg: "No compound found",
		},
		{
			name:       "inchikey first block not found",
			query:      "ZZZZZZZZZZZZZZ-XXXXXXXXXX-Y",
			wantErrMsg: "No compound(s) found",
		},
		{
			name:       "smiles no match",
			query:      "CC(O)=O",
			wantErrMsg: "No compound found",
		},
		{
			name:       "formula no match",
			query:      "Unknown",
			wantErrMsg: "No compound found",
		},
		{
			name:       "bad inchikey",
			query:      "ABCDEFGHIJKLMNO-ABCDEFGHIJ-A",
			wantErrMsg: "Malformed InChIKey, see documentation",
		},
		{
			name:       "bad inchi",
			query:      "inchi=1S/H2O",
			wantErrMsg: "Malformed InChI, see documentation",
		},
		{
			name:       "unidentified query",
			query:      "12345a",
			wantErrMsg: "Invalid query type, could not identify",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := doMatchRequest(t, `{"queries":"`+tc.query+`"}`, nil, false)
			results := parseMatchResults(t, res)

			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].MatchFound {
				t.Errorf("expected no match, but MatchFound=true")
			}
			if results[0].ErrMsg != tc.wantErrMsg {
				t.Errorf("expected error %q, got %q", tc.wantErrMsg, results[0].ErrMsg)
			}
		})
	}
}

func TestQuotedEmptyQuery(t *testing.T) {
	// Regression: `""` after quote-stripping becomes empty, which previously caused
	//   a panic in parseQueryType due to an out-of-bounds slice on an empty string
	res := doMatchRequest(t, `{"queries":"\"\""}`, nil, false)
	results := parseMatchResults(t, res)

	if len(results) != 0 {
		t.Errorf("expected 0 results for empty quoted query, got %d", len(results))
	}
}

func TestSingleDoubleQuoteQuery(t *testing.T) {
	// Regression: a lone `"` character was not stripped by the quote-removal
	//   logic (HasPrefix && HasSuffix both true for a 1-char string, causing an
	//   empty slice q[1:0]), and was not caught by the subsequent empty-string
	//   check, leading to a panic in parseQueryType.
	res := doMatchRequest(t, `{"queries":"\""}`, nil, false)
	results := parseMatchResults(t, res)

	if len(results) != 0 {
		t.Errorf("expected 0 results for single double-quote query, got %d", len(results))
	}
}

func TestMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/match", nil)
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

func TestInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

func TestEmptyQuery(t *testing.T) {
	res := doMatchRequest(t, `{"queries":""}`, nil, false)

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", res.StatusCode)
	}
}

func TestQuotedQuery(t *testing.T) {
	res := doMatchRequest(t, `{"queries":"\"O\""}`, nil, false)
	results := parseMatchResults(t, res)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 compound, got %d", len(results[0].Matches))
	}
	assertCompound(t, fakeWaterCompound(), results[0].Matches[0])
}

func TestCSVFormatQueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/match?format=csv", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if res.Header.Get("Content-Type") != "text/csv" {
		t.Errorf("expected Content-Type text/csv, got %s", res.Header.Get("Content-Type"))
	}
}

func TestCSVNoMatchResponse(t *testing.T) {
	res := doMatchRequest(t, `{"queries":"InChI=1S/NOTHING"}`, map[string]string{"Accept": "text/csv"}, false)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	records, err := csv.NewReader(res.Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to read CSV: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 rows (header + 1 no-match row), got %d", len(records))
	}

	row := records[1]
	if row[2] != "false" {
		t.Errorf("expected found_match=false, got %s", row[2])
	}
	// All compound-specific fields should be empty
	for i, field := range row[5:] {
		if field != "" {
			t.Errorf("expected empty compound field at index %d, got %q", i+5, field)
		}
	}
}

func TestCSVFormatResponse(t *testing.T) {
	res := doMatchRequest(t, `{"queries":"O"}`, map[string]string{"Accept": "text/csv"}, false)

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
		"pubchem_cid", "inchikey", "inchi", "smiles", "compound_name",
		"molecular_formula", "monoisotopic_mass", "literature_count", "patent_count",
	}
	if diff := cmp.Diff(expectedHeader, records[0]); diff != "" {
		t.Errorf("CSV header mismatch (-want +got):\n%s", diff)
	}

	// Check data row
	expectedData := []string{
		"O", "smiles", "true", "Exact SMILES", "",
		"1", "MYFAKEINCHIKEY-ISRIGHTHER-E", "InChI=1S/H2O/h1H2", "O", "Water", "H2O", "100", "10", "2",
	}
	if diff := cmp.Diff(expectedData, records[1]); diff != "" {
		t.Errorf("CSV data row mismatch (-want +got):\n%s", diff)
	}
}

func TestCSVCommaInQuery(t *testing.T) {
	// Regression: InChI queries contain commas; the query field must be quoted
	// so csv.NewReader does not split it mid-field.
	const inchi = "InChI=1S/C10H18O/c1-9(2)7-4-5-10(9,3)8(11)6-7/h7-8,11H,4-6H2,1-3H3/t7-,8+,10+/m0/s1"
	res := doMatchRequest(t, `{"queries":"`+inchi+`"}`, map[string]string{"Accept": "text/csv"}, false)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	records, err := csv.NewReader(res.Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV (likely unquoted comma in query field): %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 rows (header + 1 no-match row), got %d", len(records))
	}

	if records[1][0] != inchi {
		t.Errorf("query field mangled by commas: want %q, got %q", inchi, records[1][0])
	}
}

func TestMatchPubChemID(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantFound   bool
		wantErrMsg  string
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
			wantErrMsg: "No compound found",
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

// TestMatchErrorPaths verifies that all matchXxx functions return an internal
// error message (rather than panicking) when the underlying database is closed.
// Each sub-test uses a fresh index so closing it does not affect other tests.
func TestMatchErrorPaths(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"inchi", `{"queries":"InChI=1S/H2O/h1H2"}`},
		{"inchikey_exact", `{"queries":"MYFAKEINCHIKEY-ISRIGHTHER-E"}`},
		// Full InChIKey miss → falls through to first-block lookup
		{"inchikey_firstblock", `{"queries":"MYFAKEINCHIKEY-NOTNOTNOTN-O"}`},
		// C=O contains '=' so smilesGuaranteePattern matches → type smiles → matchSmiles
		{"smiles_direct", `{"queries":"C=O"}`},
		{"formula", `{"queries":"H2O"}`},
		{"smiles_or_formula", `{"queries":"CH4"}`},
		// Numeric query → type pubchem_id → matchPubChemID
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

