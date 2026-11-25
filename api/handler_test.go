package api

import (
	"ctslite/model"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

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

func TestQueryLineLimitExceeded(t *testing.T) {
	// Blank and bare-quote lines make no query
	if got := countQueryLines("O\n\n  \"\" \n\"\" water\nC"); got != 3 {
		t.Errorf("expected 3 query lines, got %d", got)
	}

	// Rejected by the line count before splitting
	queries := strings.TrimRight(strings.Repeat(`O\n`, 100001), `\n`)
	res := doMatchRequest(t, `{"queries":"`+queries+`"}`, nil, false)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for over-limit lines, got %d", res.StatusCode)
	}
}

func TestSplitQueryLimitExceeded(t *testing.T) {
	// 50,001 lines pass the line count, but each splits into two HMDB IDs
	queries := strings.TrimRight(strings.Repeat(`HMDB1 HMDB2\n`, 50001), `\n`)
	res := doMatchRequest(t, `{"queries":"`+queries+`"}`, nil, false)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for lines splitting over the limit, got %d", res.StatusCode)
	}
	if body, _ := io.ReadAll(res.Body); !strings.Contains(string(body), "more than 100000 identifiers") {
		t.Errorf("unexpected error body: %s", body)
	}
}

func TestSplitQueriesStopsPastLimit(t *testing.T) {
	// Stops after the second line takes it past 3, the other 8 lines are never split
	raw := strings.TrimRight(strings.Repeat("HMDB1 HMDB2\n", 10), "\n")
	if queries, _ := splitQueries(mockIndex, raw, 3, ""); len(queries) != 4 {
		t.Errorf("expected splitting to stop at 4 queries, got %d", len(queries))
	}
}

func TestSplitQueriesMixedLines(t *testing.T) {
	type q struct{ query, queryType string }
	cases := []struct {
		line string
		want []q
	}{
		// Over maxNameWords, so every word is its own query
		{"HMDB 00143 HMDB 00213 hello methane HMDB 5 HMDB -1 HMDB 0", []q{
			{"HMDB", "bad_hmdb"}, {"00143", "bad_pubchem_id"}, {"HMDB", "bad_hmdb"}, {"00213", "bad_pubchem_id"},
			{"hello", "unidentified"}, {"methane", "unidentified"}, {"HMDB", "bad_hmdb"}, {"5", "pubchem_id"},
			{"HMDB", "bad_hmdb"}, {"-1", "smiles"}, {"HMDB", "bad_hmdb"}, {"0", "bad_pubchem_id"},
		}},
		// A spaced HMDB ID is never joined, the bare prefix is a strong type so the line splits
		{"HMDB 00143", []q{{"HMDB", "bad_hmdb"}, {"00143", "bad_pubchem_id"}}},
		{"HMDB 00143 HMDB 00213", []q{{"HMDB", "bad_hmdb"}, {"00143", "bad_pubchem_id"}, {"HMDB", "bad_hmdb"}, {"00213", "bad_pubchem_id"}}},
		{"HMDB 5", []q{{"HMDB", "bad_hmdb"}, {"5", "pubchem_id"}}},
		{"HMDB -1", []q{{"HMDB", "bad_hmdb"}, {"-1", "smiles"}}},
		{"HMDB 0", []q{{"HMDB", "bad_hmdb"}, {"0", "bad_pubchem_id"}}},
		{"HMDB00143 HMDB00213", []q{{"HMDB00143", "hmdb"}, {"HMDB00213", "hmdb"}}},
		// One strong identifier splits a line that would otherwise be a name
		{"methane HMDB00143", []q{{"methane", "unidentified"}, {"HMDB00143", "hmdb"}}},
		// Words that are not all identifiers stay one name query
		{"hello methane", []q{{"hello methane", "unidentified"}}},
		{"methane 5", []q{{"methane 5", "unidentified"}}},
		{"water 0", []q{{"water 0", "unidentified"}}},
		{"hello -1", []q{{"hello -1", "unidentified"}}},
		{"marsh gas", []q{{"marsh gas", "unidentified"}}},
	}

	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			queries, _ := splitQueries(mockIndex, tc.line, maxQueries, "")
			if len(queries) != len(tc.want) {
				t.Fatalf("got %d queries %q, want %d", len(queries), queries, len(tc.want))
			}
			for i, w := range tc.want {
				if got := (q{queries[i], parseQueryType(queries[i])}); got != w {
					t.Errorf("query %d: got %+v, want %+v", i, got, w)
				}
			}
		})
	}
}

func TestSplitQueriesQuotes(t *testing.T) {
	cases := map[string][]string{
		`"tween 80" 2244 HMDB0000001`: {"tween 80", "2244", "HMDB0000001"},
		`"  hello   world  " water`:   {"hello world", "water"},
		`" hello world " water`:       {"hello world", "water"},
		`"hello world`:                {"hello world"},
		`hello world"`:                {"hello world"},
		`"" water`:                    {"water"},
		`abc"def ghi"`:                {`abc"def ghi`},
		`"tween 80"s`:                 {`tween 80"s`},
		`"marsh gas" water`:           {"marsh gas", "water"},
	}
	for line, want := range cases {
		if got, _ := splitQueries(mockIndex, line, maxQueries, ""); !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", line, got, want)
		}
	}

	// Quotes apply under a forced type too
	if got, _ := splitQueries(mockIndex, `"tween 80" 2244`, maxQueries, "pubchem_id"); !slices.Equal(got, []string{"tween 80", "2244"}) {
		t.Errorf("forced pubchem_id: got %q", got)
	}
	if got, _ := splitQueries(mockIndex, `"water" "marsh gas"`, maxQueries, "name"); !slices.Equal(got, []string{"water", "marsh gas"}) {
		t.Errorf("forced name: got %q", got)
	}
}

func TestMultiEntryNote(t *testing.T) {
	// A whole line holding an identifier gets the note on a miss, a plain name miss does not
	results := parseMatchResults(t, doMatchRequest(t, `{"queries":"2244 CCO caffeine\nhello methane"}`, nil, false))
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Query != "2244 CCO caffeine" || results[0].ErrMsg != multiEntryNote {
		t.Errorf("got %q %q, want the whole line with the note", results[0].Query, results[0].ErrMsg)
	}
	if results[1].ErrMsg == multiEntryNote {
		t.Errorf("plain name miss got the note")
	}
}

func TestForcedQueryType(t *testing.T) {
	forcedResults := func(queryType, queries string) []*model.SingleResult {
		req := httptest.NewRequest(http.MethodPost, "/match?query_type="+queryType, strings.NewReader(`{"queries":"`+queries+`"}`))
		w := httptest.NewRecorder()
		Match(mockIndex, w, req)
		return parseMatchResults(t, w.Result())
	}
	type row struct{ queryType, errMsg string }
	check := func(results []*model.SingleResult, want []row) {
		t.Helper()
		if len(results) != len(want) {
			t.Fatalf("expected %d results, got %d", len(want), len(results))
		}
		for i, w := range want {
			if got := (row{results[i].QueryType, results[i].ErrMsg}); got != w {
				t.Errorf("result %d (%q): got %+v, want %+v", i, results[i].Query, got, w)
			}
		}
	}

	// A name is one query per line, and a CID can never be one
	check(forcedResults("name", `water\nmethane water\n1`), []row{
		{"name", ""},
		{"name", "No compound found (forced query type: name)"},
		{"name", "Not a valid name, see documentation (forced query type: name)"},
	})

	// A CID is rejected before the PubChem fallback, which would say it was unavailable
	check(forcedResults("smiles", `O 1`), []row{
		{"smiles", ""},
		{"smiles", "Not a valid SMILES, see documentation (forced query type: smiles)"},
	})

	// A quoted span stays one query and is rejected as a whole
	check(forcedResults("pubchem_id", `\"tween 80\" 1`), []row{
		{"pubchem_id", "Not a valid PubChem CID, see documentation (forced query type: pubchem_id)"},
		{"pubchem_id", ""},
	})

	// A broken form of the forced type keeps its detected type
	check(forcedResults("cas", `50-00-0 50-00-1`), []row{
		{"cas", ""},
		{"bad_cas", "Malformed CAS ID, see documentation (forced query type: cas)"},
	})

	// No name fallback for a word under a forced structure type
	check(forcedResults("formula", `CH4 CCO water`), []row{
		{"formula", ""},
		{"formula", "Not a valid molecular formula, see documentation (forced query type: formula)"},
		{"formula", "Not a valid molecular formula, see documentation (forced query type: formula)"},
	})

	req := httptest.NewRequest(http.MethodPost, "/match?query_type=bogus", strings.NewReader(`{"queries":"O"}`))
	w := httptest.NewRecorder()
	Match(mockIndex, w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown query_type, got %d", w.Result().StatusCode)
	}
}

func TestArrayQueries(t *testing.T) {
	// One query per element, never split, even past maxNameWords
	res := doMatchRequest(t, `{"queries":["O C", "water", "aa bb cc dd ee ff gg hh ii jj kk", "\"C\""]}`, nil, false)
	results := parseMatchResults(t, res)
	want := []string{"O C", "water", "aa bb cc dd ee ff gg hh ii jj kk", "C"}
	if len(results) != len(want) {
		t.Fatalf("expected %d results, got %d", len(want), len(results))
	}
	for i, q := range want {
		if results[i].Query != q {
			t.Errorf("results[%d]: got query %q, want %q", i, results[i].Query, q)
		}
	}

	// A blank element would shift later results, so it fails the request
	for _, payload := range []string{`{"queries":["O", "  "]}`, `{"queries":["O", "\"\""]}`, `{"queries":[]}`, `{"queries":["O", 1]}`} {
		if res := doMatchRequest(t, payload, nil, false); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", payload, res.StatusCode)
		}
	}
}

func TestLongLineAlwaysSplits(t *testing.T) {
	// Over maxNameWords words splits even when no word is an identifier
	res := doMatchRequest(t, `{"queries":"aa bb cc dd ee ff gg hh ii jj kk"}`, nil, false)
	if got := len(parseMatchResults(t, res)); got != 11 {
		t.Errorf("expected 11 results for an 11-word line, got %d", got)
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
