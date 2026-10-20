package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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

func TestMultiQueryOrderPreserved(t *testing.T) {
	// 10 queries across all supported query types in a deliberate non-alphabetical
	// order. Goroutines may complete in any order, but results must match input order.
	const payload = `{"queries":"1 C=O InChI=1S/CH4/h1H4 MYFAKEINCHIKEY-ISRIGHTHER-E 2 O FAKEFORMALDEHY-FAKEFRMALD-E InChI=1S/H2O/h1H2 3 C"}`
	res := doMatchRequest(t, payload, nil, false)
	results := parseMatchResults(t, res)

	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}

	want := []*model.Compound{
		fakeWaterCompound(),   // "1"                            pubchem_id
		fakeFormaldehyde(),    // "C=O"                          smiles
		fakeMethaneCompound(), // "InChI=1S/CH4/h1H4"            inchi
		fakeWaterCompound(),   // "MYFAKEINCHIKEY-ISRIGHTHER-E"  inchikey
		fakeMethaneCompound(), // "2"                            pubchem_id
		fakeWaterCompound(),   // "O"                            smiles
		fakeFormaldehyde(),    // "FAKEFORMALDEHY-FAKEFRMALD-E"  inchikey
		fakeWaterCompound(),   // "InChI=1S/H2O/h1H2"            inchi
		fakeFormaldehyde(),    // "3"                            pubchem_id
		fakeMethaneCompound(), // "C"                            smiles_or_formula
	}
	for i, w := range want {
		if !results[i].MatchFound {
			t.Errorf("results[%d]: expected match for query %q, got none", i, results[i].Query)
			continue
		}
		assertCompound(t, w, results[i].Matches[0])
	}
}

func TestMatchErrors(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantErrMsg string
	}{
		{
			// A local miss now always attempts a live PubChem fallback
			// fallback is disabled in this test binary so the misses all fail fast
			name:       "inchi no match",
			query:      "InChI=1S/NOTHING",
			wantErrMsg: "No local match; PubChem Live lookup unavailable",
		},
		{
			name:       "inchikey first block not found",
			query:      "ZZZZZZZZZZZZZZ-XXXXXXXXXX-Y",
			wantErrMsg: "No local match; PubChem Live lookup unavailable",
		},
		{
			name:       "smiles no match",
			query:      "CC(O)=O",
			wantErrMsg: "No local match; PubChem Live lookup unavailable",
		},
		{
			// A real formula shape that is absent from the test dataset
			name:       "formula no match",
			query:      "C99H99N99O99",
			wantErrMsg: "No local match; PubChem Live lookup unavailable",
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
			name:       "bad inchi mixed case",
			query:      "Inchi=1S/H2O",
			wantErrMsg: "Malformed InChI, see documentation",
		},
		{
			name:       "unidentified query",
			query:      "12345a",
			wantErrMsg: "Invalid query type, could not identify, see documentation",
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
	// A query of only bare quotes is treated like a blank query
	for _, payload := range []string{`{"queries":"\"\""}`, `{"queries":"\"\"\""}`, `{"queries":"\" \"\" \"\"\""}`} {
		res := doMatchRequest(t, payload, nil, false)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", payload, res.StatusCode)
		}
	}
}

func TestSingleDoubleQuoteQuery(t *testing.T) {
	// Regression: a lone `"` character was not stripped by the quote-removal
	//   logic (HasPrefix && HasSuffix both true for a 1-char string, causing an
	//   empty slice q[1:0]), and was not caught by the subsequent empty-string
	//   check, leading to a panic in parseQueryType.
	res := doMatchRequest(t, `{"queries":"\""}`, nil, false)

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for single double-quote query, got %d", res.StatusCode)
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

func TestQueryLimitExceeded(t *testing.T) {
	queries := strings.TrimRight(strings.Repeat("O ", 100001), " ")
	res := doMatchRequest(t, `{"queries":"`+queries+`"}`, nil, false)

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for over-limit query, got %d", res.StatusCode)
	}
}

func TestClassyFireQueryLimitExceeded(t *testing.T) {
	// ClassyFire enabled limits queries to 1,000 entries/identifiers
	queries := strings.TrimRight(strings.Repeat("O ", 1001), " ")
	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"`+queries+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for >1,000 entries with ClassyFire enabled, got %d", w.Result().StatusCode)
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
	if row[3] != "false" {
		t.Errorf("expected found_match=false, got %s", row[3])
	}
	// All compound-specific fields should be empty
	for i, field := range row[6:] {
		if field != "" {
			t.Errorf("expected empty compound field at index %d, got %q", i+6, field)
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
	if diff := cmp.Diff(CSVHeader, records[0]); diff != "" {
		t.Errorf("CSV header mismatch (-want +got):\n%s", diff)
	}

	// Check data row
	expectedData := []string{
		"O", "smiles", "", "true", "Exact SMILES", "",
		"1", "MYFAKEINCHIKEY-ISRIGHTHER-E", "InChI=1S/H2O/h1H2", "O", "Water", "H2O", "100", "10", "2", "6",
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

func TestClassyFireDisabledByDefault(t *testing.T) {
	called := false
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		called = true
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	res := doMatchRequest(t, `{"queries":"O"}`, nil, false)
	parseMatchResults(t, res)

	if called {
		t.Error("expected ClassyFire fetcher not to be called when classyfire param is absent")
	}
}

func TestClassyFireEnabledAttachesClassification(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	results := parseMatchResults(t, w.Result())
	if len(results) != 1 || len(results[0].Matches) != 1 {
		t.Fatalf("expected 1 result with 1 match")
	}
	cf := results[0].Matches[0].ClassyFire
	if cf == nil {
		t.Fatal("expected ClassyFire info, got nil")
	}
	if cf.Kingdom != "Organic compounds" {
		t.Errorf("Kingdom: want %q, got %q", "Organic compounds", cf.Kingdom)
	}
}

func TestClassyFireNoMatchDoesNotCallFetcher(t *testing.T) {
	called := false
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		called = true
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"InChI=1S/NOTHING"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	parseMatchResults(t, w.Result())
	if called {
		t.Error("expected ClassyFire fetcher not to be called for unmatched query")
	}
}

func TestClassyFireFetchErrorDoesNotFailRequest(t *testing.T) {
	// Even if ClassyFire is unavailable, the match result must still be returned.
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{mode: cfbRetryLimited, errMsg: "ClassyFire service unreachable"}, errors.New("simulated network error")
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even on ClassyFire error, got %d", res.StatusCode)
	}
	results := parseMatchResults(t, res)
	if !results[0].MatchFound {
		t.Error("expected compound match regardless of ClassyFire failure")
	}
	// The failure must be surfaced via the Error field, not dropped.
	cf := results[0].Matches[0].ClassyFire
	if cf == nil || cf.Error == "" {
		t.Errorf("expected ClassyFire error surfaced after fetch failure, got %+v", cf)
	}
}

func TestClassyFireClassifiesUniqueInChIKeysOnce(t *testing.T) {
	calls := 0
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		calls++ // non-stream enrichment runs sequentially, so a plain counter is safe
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	queries := strings.TrimRight(strings.Repeat("O ", 10), " ") // 10x water
	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"`+queries+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	results := parseMatchResults(t, w.Result())
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}
	if calls != 1 {
		t.Errorf("expected 1 ClassyFire lookup for 10 identical InChIKeys, got %d", calls)
	}
	// Every occurrence should still receive the shared classification.
	for i, r := range results {
		if len(r.Matches) == 0 || r.Matches[0].ClassyFire == nil {
			t.Errorf("result[%d]: expected ClassyFire info attached", i)
		}
	}
}

func TestMatchStreamingClassyFire(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true&stream=true", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("expected NDJSON content type, got %q", ct)
	}

	type streamMsg struct {
		Type     string                `json:"type"`
		Results  []*model.SingleResult `json:"results"`
		Unique   int                   `json:"unique"`
		InChIKey string                `json:"inchikey"`
		Info     *model.ClassyFireInfo `json:"info"`
	}

	body, _ := io.ReadAll(res.Body)
	var msgs []streamMsg
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var m streamMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}

	if len(msgs) < 3 {
		t.Fatalf("expected at least matches+classyfire+done lines, got %d", len(msgs))
	}
	if msgs[0].Type != "matches" {
		t.Errorf("first line: want type=matches, got %q", msgs[0].Type)
	}
	if msgs[0].Unique != 1 {
		t.Errorf("matches line: want unique=1, got %d", msgs[0].Unique)
	}
	if len(msgs[0].Results) != 1 || len(msgs[0].Results[0].Matches) != 1 {
		t.Fatalf("matches line should carry the single match")
	}
	if msgs[len(msgs)-1].Type != "done" {
		t.Errorf("last line: want type=done, got %q", msgs[len(msgs)-1].Type)
	}

	var sawClassyfire bool
	for _, m := range msgs {
		if m.Type == "classyfire" {
			sawClassyfire = true
			if m.Info == nil || m.Info.Kingdom != "Organic compounds" {
				t.Errorf("classyfire line missing expected info: %+v", m.Info)
			}
		}
	}
	if !sawClassyfire {
		t.Error("expected at least one classyfire line in the stream")
	}
}

func TestMatchStreamingRespectsClassyFireLimit(t *testing.T) {
	// The 1000-entry cap is validated before streaming begins, so it still
	// returns a 400 (not a 200 stream) even with stream=true.
	queries := strings.TrimRight(strings.Repeat("O ", 1001), " ")
	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true&stream=true", strings.NewReader(`{"queries":"`+queries+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for >1,000 entries even when streaming, got %d", w.Result().StatusCode)
	}
}

func TestClassyFireCSVHeader(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/csv")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	records, err := csv.NewReader(res.Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + data row, got %d rows", len(records))
	}

	// ClassyFire columns must be appended to the standard header
	wantSuffix := []string{
		"classyfire_kingdom", "classyfire_superclass", "classyfire_class",
		"classyfire_subclass", "classyfire_direct_parent", "classyfire_description",
		"classyfire_error",
	}
	header := records[0]
	if len(header) != 23 {
		t.Fatalf("expected 23 CSV columns with classyfire enabled, got %d", len(header))
	}
	for i, want := range wantSuffix {
		got := header[16+i]
		if got != want {
			t.Errorf("header[%d]: want %q, got %q", 16+i, want, got)
		}
	}

	// Data row must contain ClassyFire values
	row := records[1]
	if row[16] != "Organic compounds" {
		t.Errorf("classyfire_kingdom: want %q, got %q", "Organic compounds", row[16])
	}
}

func TestClassyFireCSVNoExtraColumnsWhenDisabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/match", strings.NewReader(`{"queries":"O"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/csv")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	records, err := csv.NewReader(w.Result().Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV: %v", err)
	}
	if len(records[0]) != 16 {
		t.Errorf("expected 16 columns without classyfire, got %d", len(records[0]))
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

// DerivedInChIKey field should never appear in results, only used internally for
// PubChem fallback to reuse the rdkit conversion if we already performed it
func TestDerivedInChIKeyNeverSerialized(t *testing.T) {
	results := []*model.SingleResult{{
		Query:           "CCO",
		QueryType:       "smiles",
		ConvertedQuery:  "VISIBLEKEYAAAA-VISIBLEKEY-V",
		ErrMsg:          "No compound found",
		DerivedInChIKey: "SENTINELKEYAAA-SENTINELKK-S",
	}}

	assertHidden := func(format, out string) {
		t.Helper()
		// The control value proves the output really was rendered
		if !strings.Contains(out, "VISIBLEKEY") {
			t.Fatalf("%s: control value missing, assertion proves nothing", format)
		}
		if strings.Contains(out, "SENTINEL") {
			t.Errorf("%s: DerivedInChIKey value leaked into output", format)
		}
		if strings.Contains(strings.ToLower(out), "derived") {
			t.Errorf("%s: DerivedInChIKey field name leaked into output", format)
		}
	}

	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("failed to marshal results: %v", err)
	}
	assertHidden("JSON", string(encoded))

	for _, classyfireEnabled := range []bool{false, true} {
		w := httptest.NewRecorder()
		if err := writeResultsAsCSV(w, results, classyfireEnabled); err != nil {
			t.Fatalf("failed to write CSV: %v", err)
		}
		body, _ := io.ReadAll(w.Result().Body)
		assertHidden(fmt.Sprintf("CSV(classyfire=%v)", classyfireEnabled), string(body))
	}
}

// A normal request (ClassyFire off) must not emit a classyfire field in the JSON
func TestMatchResponseOmitsClassyFireWhenDisabled(t *testing.T) {
	res := doMatchRequest(t, `{"queries":"O"}`, nil, false)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "classyfire") {
		t.Errorf("expected no classyfire key when disabled, got body: %s", body)
	}
}

// A no-match query with ClassyFire enabled has no classyfire object (nothing to classify)
func TestClassyFireEnabledNonMatchHasNoClassyFireObject(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	res := w.Result()
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "classyfire") {
		t.Errorf("expected no classyfire object for a no-match, got body: %s", body)
	}
}

// A no-match CSV row with ClassyFire enabled still carries the 7 empty classyfire columns
func TestClassyFireCSVNoMatchRowHasEmptyColumns(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true", strings.NewReader(`{"queries":"ZZZZZZZZZZZZZZ-ZZZZZZZZZZ-Z"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/csv")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	records, err := csv.NewReader(w.Result().Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + no-match row, got %d rows", len(records))
	}
	row := records[1]
	if len(row) != 23 {
		t.Fatalf("expected 23 columns on the no-match row, got %d", len(row))
	}
	if row[3] != "false" {
		t.Errorf("expected found_match=false, got %q", row[3])
	}
	for i := 15; i < 22; i++ {
		if row[i] != "" {
			t.Errorf("classyfire column %d should be empty on a no-match, got %q", i, row[i])
		}
	}
}

// Streaming caps a query at the top hit: extras carry the cap note and only the top hit is classified
func TestStreamMatchResultsCapsAndStreams(t *testing.T) {
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	matches := make([]*model.Compound, 5)
	for i := range matches {
		matches[i] = &model.Compound{InChIKey: string(rune('A'+i)) + "YYVLZVUVIJVGH-UHFFFAOYSA-N"}
	}
	results := []*model.SingleResult{{MatchFound: true, Matches: matches}}

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true&stream=true", nil)
	w := httptest.NewRecorder()
	streamMatchResults(w, req, results, true, func(func(int)) {})

	type streamMsg struct {
		Type    string                `json:"type"`
		Results []*model.SingleResult `json:"results"`
		Unique  int                   `json:"unique"`
	}
	var msgs []streamMsg
	var classyfireLines int
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var m streamMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		if m.Type == "classyfire" {
			classyfireLines++
		}
		msgs = append(msgs, m)
	}

	if msgs[0].Type != "matches" {
		t.Fatalf("first line: want matches, got %q", msgs[0].Type)
	}
	if msgs[0].Unique != cfbMaxMatchesPerQuery {
		t.Errorf("unique: want %d, got %d", cfbMaxMatchesPerQuery, msgs[0].Unique)
	}
	if classyfireLines != cfbMaxMatchesPerQuery {
		t.Errorf("expected %d classyfire lines, got %d", cfbMaxMatchesPerQuery, classyfireLines)
	}
	if msgs[len(msgs)-1].Type != "done" {
		t.Errorf("last line: want done, got %q", msgs[len(msgs)-1].Type)
	}

	// Matches beyond the top hit must carry the capped note in the initial matches line
	streamed := msgs[0].Results[0].Matches
	for i := cfbMaxMatchesPerQuery; i < len(streamed); i++ {
		cf := streamed[i].ClassyFire
		if cf == nil || cf.Error != cfbCappedNote {
			t.Errorf("match[%d]: expected capped note, got %+v", i, cf)
		}
	}
}

// ClassyFire starts on local hits while the fallback is still running, and the
// PubChem hits join the same stream once each of them settles. The total starts
// as an upper bound counting every pending row and shrinks when one misses
func TestStreamMatchResultsClassifiesDuringFallback(t *testing.T) {
	localKey, liveKey, missKey := "AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N", "CYYVLZVUVIJVGH-UHFFFAOYSA-N"
	localClassified := make(chan struct{})
	mockClassyFire(t, func(inchikey string) (cfbFetch, error) {
		if inchikey == localKey {
			close(localClassified)
		}
		return cfbFetch{info: fakeClassyFireInfo()}, nil
	})

	results := []*model.SingleResult{
		{Query: localKey, QueryType: "inchikey", MatchFound: true, Matches: []*model.Compound{{InChIKey: localKey}}},
		{Query: liveKey, QueryType: "inchikey"},
		{Query: missKey, QueryType: "inchikey"},
	}

	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true&stream=true", nil)
	w := httptest.NewRecorder()
	streamMatchResults(w, req, results, true, func(onSettled func(int)) {
		// Simulate the fallback settling after the local key was classified
		select {
		case <-localClassified:
		case <-time.After(2 * time.Second):
			t.Error("ClassyFire did not start before the fallback settled")
		}
		onSettled(2) // PubChem has no match, so the bound drops
		results[1].MatchFound = true
		results[1].Matches = []*model.Compound{{InChIKey: liveKey}}
		onSettled(1) // a hit with a new key keeps the bound, no new classifying line
	})

	var types []string
	var uniques []int
	classified := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		var m struct {
			Type     string `json:"type"`
			Unique   int    `json:"unique"`
			InChIKey string `json:"inchikey"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		types = append(types, m.Type)
		if m.Type == "matches" || m.Type == "classifying" {
			uniques = append(uniques, m.Unique)
		}
		if m.Type == "classyfire" {
			classified[m.InChIKey] = true
		}
	}

	if !classified[localKey] || !classified[liveKey] {
		t.Errorf("expected both keys classified, got %v", classified)
	}
	// matches, then the queue join, then the miss
	if !slices.Equal(uniques, []int{3, 3, 2}) {
		t.Errorf("totals: want [3 3 2], got %v", uniques)
	}
	if types[len(types)-1] != "done" {
		t.Errorf("last line: want done, got %v", types)
	}
}

// streamTotals runs streamMatchResults with ClassyFire on and returns every
// matches/classifying total in order plus how many keys were classified
func streamTotals(t *testing.T, results []*model.SingleResult, fallback func(func(int))) (totals []int, classified int) {
	t.Helper()
	mockClassyFire(t, func(string) (cfbFetch, error) { return cfbFetch{info: fakeClassyFireInfo()}, nil })
	req := httptest.NewRequest(http.MethodPost, "/match?classyfire=true&stream=true", nil)
	w := httptest.NewRecorder()
	streamMatchResults(w, req, results, true, fallback)
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		var m struct {
			Type   string `json:"type"`
			Unique int    `json:"unique"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		switch m.Type {
		case "matches", "classifying":
			totals = append(totals, m.Unique)
		case "classyfire":
			classified++
		}
	}
	return totals, classified
}

// With every query matched locally there is nothing pending, so the total is exact from the start
func TestStreamMatchResultsAllLocalTotalIsExact(t *testing.T) {
	var results []*model.SingleResult
	for _, k := range []string{"AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N", "CYYVLZVUVIJVGH-UHFFFAOYSA-N"} {
		results = append(results, &model.SingleResult{Query: k, QueryType: "inchikey", MatchFound: true, Matches: []*model.Compound{{InChIKey: k}}})
	}

	totals, classified := streamTotals(t, results, func(func(int)) {})

	// matches, then the queue join
	if !slices.Equal(totals, []int{3, 3}) {
		t.Errorf("totals: want [3 3], got %v", totals)
	}
	if classified != 3 {
		t.Errorf("expected 3 classyfire lines, got %d", classified)
	}
}

// When no query matches locally or on PubChem the bound shrinks to 0 and nothing is classified
func TestStreamMatchResultsNoMatchesShrinksToZero(t *testing.T) {
	var results []*model.SingleResult
	for _, k := range []string{"AYYVLZVUVIJVGH-UHFFFAOYSA-N", "BYYVLZVUVIJVGH-UHFFFAOYSA-N", "CYYVLZVUVIJVGH-UHFFFAOYSA-N"} {
		results = append(results, &model.SingleResult{Query: k, QueryType: "inchikey"})
	}

	totals, classified := streamTotals(t, results, func(onSettled func(int)) {
		for i := range results {
			onSettled(i)
		}
	})

	if !slices.Equal(totals, []int{3, 2, 1, 0}) {
		t.Errorf("totals: want [3 2 1 0], got %v", totals)
	}
	if classified != 0 {
		t.Errorf("expected no classyfire lines, got %d", classified)
	}
}

// the stream sends local matches first, marks the
// fallback row pending, then replaces it with the pubchem result
func TestMatchStreamingPubChemFallback(t *testing.T) {
	resetPcbPacing(t)
	pcbClearCache(t, "pubchem_id", "2244")
	withMockPCBTransport(t, func(*http.Request) (*http.Response, error) {
		return pcbResp(http.StatusOK, pcbPropertyJSON(`{"CID":2244,"InChIKey":"BSYNRYMUTXBXSQ-UHFFFAOYSA-N","MolecularFormula":"C9H8O4"}`)), nil
	})

	req := httptest.NewRequest(http.MethodPost, "/match?stream=true", strings.NewReader(`{"queries":"1 2244"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)

	if ct := w.Result().Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("expected NDJSON content type, got %q", ct)
	}

	type streamMsg struct {
		Type    string                `json:"type"`
		Results []*model.SingleResult `json:"results"`
		Index   int                   `json:"index"`
		Result  *model.SingleResult   `json:"result"`
	}
	var msgs []streamMsg
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		var m streamMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}

	if len(msgs) != 3 || msgs[0].Type != "matches" || msgs[1].Type != "pubchem" || msgs[2].Type != "done" {
		t.Fatalf("want matches, pubchem, done lines, got %+v", msgs)
	}
	local, waiting := msgs[0].Results[0], msgs[0].Results[1]
	if !local.MatchFound || local.Pending {
		t.Errorf("local hit should arrive found and not pending: %+v", local)
	}
	if !waiting.Pending {
		t.Error("the fallback row should be pending in the matches line")
	}
	if msgs[1].Index != 1 || msgs[1].Result == nil || !msgs[1].Result.MatchFound || msgs[1].Result.Pending {
		t.Errorf("pubchem line should carry the found, settled result at index 1: %+v", msgs[1])
	}
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
