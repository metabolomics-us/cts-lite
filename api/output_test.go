package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

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
		"7732-18-5", "HMDB0002111;HMDB0062587",
	}
	if diff := cmp.Diff(expectedData, records[1]); diff != "" {
		t.Errorf("CSV data row mismatch (-want +got):\n%s", diff)
	}
}

func TestCSVHeaderKeepsIDColumnsWithoutIDs(t *testing.T) {
	// No match carries CAS or HMDB IDs, the columns must still be there with empty cells
	results := []*model.SingleResult{{
		Query: "C", QueryType: "smiles", MatchFound: true, MatchLevel: "Exact SMILES",
		Matches: []*model.Compound{{Identifier: "2"}, {Identifier: "3"}},
	}}
	w := httptest.NewRecorder()
	if err := writeResultsAsCSV(w, results, false); err != nil {
		t.Fatalf("writeResultsAsCSV: %v", err)
	}

	records, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatalf("failed to read CSV: %v", err)
	}
	if diff := cmp.Diff(CSVHeader, records[0]); diff != "" {
		t.Errorf("CSV header mismatch (-want +got):\n%s", diff)
	}
	for _, row := range records[1:] {
		if got := row[len(row)-2:]; got[0] != "" || got[1] != "" {
			t.Errorf("expected empty cas_ids and hmdb_ids, got %q", got)
		}
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
