package api

import (
	"ctslite/model"
	"ctslite/telemetry"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

var inchikeyPattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)
var badInchikeyPattern = regexp.MustCompile(`^[a-zA-Z]{12,16}-[a-zA-Z]{9,11}-[a-zA-Z]{0,2}$`)

var CSVHeader = []string{
	"query", "query_type", "converted_query", "found_match", "match_level", "error_message",
	"pubchem_cid", "inchikey", "inchi", "smiles", "compound_name",
	"molecular_formula", "exact_mass", "literature_count", "patent_count", "annotation_type_count",
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// maxPubChemID is the highest CID possible with plenty of wiggle room.
// As of 2026, there compounds with a CID of ~180 million
const maxPubChemID = 500_000_000

// isValidPubChemID reports whether an all-digit query can be a real CID
func isValidPubChemID(s string) bool {
	// reject leading zeroes or 0 itself
	if s == "" || s[0] == '0' {
		return false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n <= maxPubChemID
}

// smilesPunctuation is the set of characters that appear in SMILES but never in a
// molecular formula, so any query containing one is unambiguously SMILES.
// Plus (+) and minus (-) are left out because formulas carry them as a charge suffix
const smilesPunctuation = "=#/\\:.@[]()"

// Element symbols are one uppercase letter plus an optional lowercase letter,
// so they pack into an index: the uppercase letter picks the row and the
// lowercase letter the column, with the last column meaning "single letter"
const (
	elementCols      = 27
	elementTableSize = 26 * elementCols
	noLowercase      = elementCols - 1
	hydrogenCode     = int('H'-'A')*elementCols + noLowercase
)

// maxDistinctElements bounds the repeat-detection scratch array. Overflowing
// it is harmless: a query with more distinct elements than this cannot be
// SMILES anyway, since only ten elements may appear unbracketed
const maxDistinctElements = 12

const periodicTable = "H He Li Be B C N O F Ne Na Mg Al Si P S Cl Ar K Ca Sc Ti V Cr Mn Fe Co Ni Cu Zn " +
	"Ga Ge As Se Br Kr Rb Sr Y Zr Nb Mo Tc Ru Rh Pd Ag Cd In Sn Sb Te I Xe Cs Ba La Ce Pr Nd Pm Sm Eu " +
	"Gd Tb Dy Ho Er Tm Yb Lu Hf Ta W Re Os Ir Pt Au Hg Tl Pb Bi Po At Rn Fr Ra Ac Th Pa U Np Pu Am Cm " +
	"Bk Cf Es Fm Md No Lr Rf Db Sg Bh Hs Mt Ds Rg Cn Nh Fl Mc Lv Ts Og"

// organicSubset are the elements SMILES writes without brackets.
// Every other element must appear as [Xx]
const organicSubset = "B C N O P S F Cl Br I"

// smilesAlphabet is every character a punctuation free SMILES can contain
const smilesAlphabet = "BCNOPSFIbclnoprs0123456789%-"

var isElementSymbol [elementTableSize]bool
var isOrganicSubset [elementTableSize]bool
var isSmilesChar [256]bool

func init() {
	for _, symbol := range strings.Fields(periodicTable) {
		isElementSymbol[packElementSymbol(symbol)] = true
	}
	for _, symbol := range strings.Fields(organicSubset) {
		isOrganicSubset[packElementSymbol(symbol)] = true
	}
	for i := 0; i < len(smilesAlphabet); i++ {
		isSmilesChar[smilesAlphabet[i]] = true
	}
}

// classifyUnreadable handles queries the formula grammar could not read. A
// string built only from SMILES characters is handed to SMILES so RDKit can
// make the final call, and anything else is not a chemical identifier at all
func classifyUnreadable(q string) string {
	if q == "" {
		return "unidentified"
	}
	for i := 0; i < len(q); i++ {
		if !isSmilesChar[q[i]] {
			return "unidentified"
		}
	}
	return "smiles"
}

func packElementSymbol(symbol string) int {
	column := noLowercase
	if len(symbol) == 2 {
		column = int(symbol[1] - 'a')
	}
	return int(symbol[0]-'A')*elementCols + column
}

// classifyElementString splits a query carrying no SMILES punctuation between
// formula and SMILES, which is the only case where both grammars can describe
// the same string. It reads the query as element symbols with optional counts
// and applies four rules in order:
//
//	a bare H is a syntax error in SMILES, so the query is a formula
//	an element repeating without hydrogen never happens in a formula
//	an element outside the organic subset would need brackets in SMILES
//	an unpaired digit is an unclosed ring bond, so it is a formula subscript
//
// Queries satisfying both grammars stay ambiguous. Anything the formula
// grammar cannot read goes to classifyUnreadable
func classifyElementString(q string) string {
	var seenCodes [maxDistinctElements]int16
	var digitCounts [10]int
	seenCount, tokenCount := 0, 0
	hasHydrogen, repeatsElement, allOrganic := false, false, true

	for i := 0; i < len(q); {
		if q[i] < 'A' || q[i] > 'Z' {
			return classifyUnreadable(q)
		}
		code := int(q[i]-'A') * elementCols
		i++
		if i < len(q) && q[i] >= 'a' && q[i] <= 'z' {
			code += int(q[i] - 'a')
			i++
		} else {
			code += noLowercase
		}
		if !isElementSymbol[code] {
			return classifyUnreadable(q)
		}
		for i < len(q) && q[i] >= '0' && q[i] <= '9' {
			digitCounts[q[i]-'0']++
			i++
		}

		tokenCount++
		if code == hydrogenCode {
			hasHydrogen = true
		}
		if !isOrganicSubset[code] {
			allOrganic = false
		}
		for s := 0; s < seenCount; s++ {
			if seenCodes[s] == int16(code) {
				repeatsElement = true
				break
			}
		}
		if seenCount < len(seenCodes) {
			seenCodes[seenCount] = int16(code)
			seenCount++
		}
	}
	if tokenCount == 0 {
		return classifyUnreadable(q)
	}

	// Ring closure labels always pair up, so an odd count means the digit is a
	// formula subscript rather than a ring bond
	unpairedDigit := false
	for _, n := range digitCounts {
		if n%2 != 0 {
			unpairedDigit = true
			break
		}
	}

	switch {
	case hasHydrogen:
		return "formula"
	case repeatsElement:
		return "smiles"
	case allOrganic && !unpairedDigit:
		return "smiles_or_formula"
	default:
		return "formula"
	}
}

// splitChargeSuffix splits a trailing sign and optional count such as -, +2 or
// -3 off a query, reporting false when the query does not end in one or the
// sign is all there is
func splitChargeSuffix(q string) (body string, sign byte, digits string, ok bool) {
	i := len(q)
	for i > 0 && q[i-1] >= '0' && q[i-1] <= '9' {
		i--
	}
	if i < 2 || (q[i-1] != '+' && q[i-1] != '-') {
		return "", 0, "", false
	}
	return q[:i-1], q[i-1], q[i:], true
}

// classifyChargeSuffix handles a query ending in a charge suffix such as
// C7H5O2-, Ca+2 or O4P-3, reporting false when the query has no suffix or the
// part before it is not formula shaped. A plus never appears outside brackets
// in SMILES and a bare trailing minus is a dangling bond, so both settle the
// query as a formula. A minus followed by digits can also be a bond into ring
// closures, as in C1CCCCC-1, so the query is reread without the minus and
// stays ambiguous when that reading still allows SMILES
func classifyChargeSuffix(q string) (string, bool) {
	body, sign, digits, ok := splitChargeSuffix(q)
	if !ok || strings.ContainsAny(body, "+-") {
		return "", false
	}
	if bodyType := classifyElementString(body); bodyType != "formula" && bodyType != "smiles_or_formula" {
		return "", false
	}
	if sign == '+' || digits == "" {
		return "formula", true
	}
	if reread := classifyElementString(body + digits); reread == "smiles" || reread == "smiles_or_formula" {
		return "smiles_or_formula", true
	}
	return "formula", true
}

func parseQueryType(q string) string {
	// Order of cases matters here
	switch {
	case inchikeyPattern.MatchString(q):
		return "inchikey"

	case badInchikeyPattern.MatchString(q):
		return "bad_inchikey"

	case strings.HasPrefix(q, "InChI="):
		return "inchi"

	case len(q) >= 6 && strings.EqualFold(q[:6], "inchi="):
		return "bad_inchi"

	case isAllDigits(q) && isValidPubChemID(q):
		return "pubchem_id"

	case isAllDigits(q):
		return "bad_pubchem_id"

	case strings.ContainsAny(q, smilesPunctuation):
		return "smiles"

	default:
		if queryType, ok := classifyChargeSuffix(q); ok {
			return queryType
		}
		// Everything left is letters and digits, which both grammars can use
		return classifyElementString(q)
	}
}

// writeResultsAsCSV converts the results to CSV format and writes to the response writer
func writeResultsAsCSV(w http.ResponseWriter, results []*model.SingleResult, classyfireEnabled bool) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write CSV header
	header := CSVHeader
	if classyfireEnabled {
		// Clip forces append to copy rather than mutate the shared CSVHeader
		header = append(slices.Clip(header),
			"classyfire_kingdom", "classyfire_superclass", "classyfire_class",
			"classyfire_subclass", "classyfire_direct_parent", "classyfire_description",
			"classyfire_error",
		)
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	cfFields := func(cf *model.ClassyFireInfo) []string {
		if cf == nil {
			return []string{"", "", "", "", "", "", ""}
		}
		return []string{cf.Kingdom, cf.Superclass, cf.Class, cf.Subclass, cf.DirectParent, cf.Description, cf.Error}
	}

	// Write data rows
	for _, result := range results {
		if !result.MatchFound {
			// Write a single row for failed matches
			row := []string{
				result.Query,
				result.QueryType,
				result.ConvertedQuery,
				strconv.FormatBool(result.MatchFound),
				result.MatchLevel,
				result.ErrMsg,
				"", "", "", "", "", "", "", "", "", "", // Empty compound fields
			}
			if classyfireEnabled {
				row = append(row, cfFields(nil)...)
			}
			if err := writer.Write(row); err != nil {
				return fmt.Errorf("failed to write CSV row: %w", err)
			}
		} else {
			// Write one row per match
			for _, match := range result.Matches {
				row := []string{
					result.Query,
					result.QueryType,
					result.ConvertedQuery,
					strconv.FormatBool(result.MatchFound),
					result.MatchLevel,
					result.ErrMsg,
					match.Identifier,
					match.InChIKey,
					match.InChI,
					match.Smiles,
					match.CompoundName,
					match.MolecularFormula,
					strconv.FormatFloat(match.ExactMass, 'f', -1, 64),
					strconv.FormatFloat(float64(match.LiteratureCount), 'f', -1, 32),
					strconv.FormatFloat(float64(match.PatentCount), 'f', -1, 32),
					strconv.FormatFloat(float64(match.AnnotationTypeCount), 'f', -1, 32),
				}
				if classyfireEnabled {
					row = append(row, cfFields(match.ClassyFire)...)
				}
				if err := writer.Write(row); err != nil {
					return fmt.Errorf("failed to write CSV row: %w", err)
				}
			}
		}
	}

	return nil
}

// Match is the main entry point for the API
// Detects the type of query and delegates it to the corresponding matching function
func Match(index *model.PubChemIndex, w http.ResponseWriter, r *http.Request) {
	var rawQuery string

	// Parse query according to GET or POST request (GET was the old method before moving to POST)
	switch r.Method {

	case http.MethodGet:
		rawQuery = r.URL.Query().Get("q")

	case http.MethodPost:
		var request struct {
			Queries string `json:"queries"`
		}

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		rawQuery = strings.TrimSpace(request.Queries)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if rawQuery == "" {
		http.Error(w, "Query was empty", http.StatusBadRequest)
		return
	}

	// Check for request parameters
	var topHitOnly bool = r.URL.Query().Get("top_hit_only") != "false"
	var allowFirstBlockMatches bool = r.URL.Query().Get("first_block_matches") != "false"
	var classyfireEnabled bool = r.URL.Query().Get("classyfire") == "true"
	var stream bool = r.URL.Query().Get("stream") == "true"

	// Split query by space or newline (can't use comma because InChI or SMILES can contain commas)
	queries := strings.Fields(rawQuery)

	if len(queries) > 100000 {
		http.Error(w, fmt.Sprintf("Query contains %d identifiers (limit 100,000)", len(queries)), http.StatusBadRequest)
		return
	}

	// Enforce ClassyFire query limit
	if classyfireEnabled && len(queries) > 1000 {
		http.Error(w, fmt.Sprintf("Query contains %d identifiers (limit 1,000 when ClassyFire is enabled)", len(queries)), http.StatusBadRequest)
		return
	}

	results := make([]*model.SingleResult, 0, len(queries))
	var matchCount int = 0
	timeStart := time.Now()

	// One span for the whole local SQLite pass, separate from the PubChem and
	// ClassyFire spans so a trace shows where a request spent its time
	_, localSpan := telemetry.StartSpan(r.Context(), "local_match")
	localMatches := 0

	for _, q := range queries {
		q = strings.TrimSpace(q)

		// Remove surrounding double quotes if both present
		if strings.HasPrefix(q, "\"") && strings.HasSuffix(q, "\"") && len(q) > 1 {
			q = q[1 : len(q)-1]
		}

		// Handle single double quote character, and empty queries
		if q == "\"" || q == "" {
			continue
		}

		result := &model.SingleResult{
			Query:     q,
			QueryType: parseQueryType(q),
		}

		switch result.QueryType {
		case "pubchem_id":
			matchPubChemID(index, q, result, topHitOnly)

		case "inchi":
			matchInchi(index, q, result, topHitOnly)

		case "inchikey":
			matchInchiKey(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "smiles":
			matchSmiles(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "formula":
			matchFormula(index, q, result, topHitOnly)

		case "smiles_or_formula":
			matchSmilesOrFormula(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "bad_inchi":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChI, see documentation"

		case "bad_inchikey":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChIKey, see documentation"

		case "bad_pubchem_id":
			result.MatchFound = false
			result.ErrMsg = "Invalid PubChem CID, see documentation"

		case "unidentified":
			result.MatchFound = false
			result.ErrMsg = "Invalid query type, could not identify, see documentation"

		default:
			log.Printf("ERROR: An unexpected error occured when parsing the request. Query type unhandled. Query: '%s'", q)
			localSpan.End()
			http.Error(w, "An unexpected error occurred when parsing the request", http.StatusInternalServerError)
			return
		}

		if result.MatchFound {
			localMatches++
		}
		results = append(results, result)
	}

	localSpan.SetAttributes(
		attribute.Int("local_match.queries", len(results)),
		attribute.Int("local_match.matches", localMatches),
	)
	localSpan.End()

	// Every query was a bare quote, so treat the request as blank
	if len(results) == 0 {
		http.Error(w, "Query was empty", http.StatusBadRequest)
		return
	}

	// Always attempt a live PubChem lookup for anything that missed locally (except unidentified queries)
	pcb := resolvePubChemFallback(r.Context(), results, topHitOnly)
	if pcb.attempted+pcb.unparseable+pcb.skipped > 0 {
		log.Printf("PubChem fallback: %d attempted, %d found, %d not found, %d failed, %d unparseable (not sent), %d skipped (due to limit reached)",
			pcb.attempted, pcb.found, pcb.notFound, pcb.failed, pcb.unparseable, pcb.skipped)
	}

	for _, result := range results {
		if result.MatchFound {
			matchCount++
		}
	}

	duration := time.Since(timeStart)
	log.Printf("%d matches found from %d queries in %s\n", matchCount, len(queries), time.Since(timeStart).Round(time.Millisecond))
	telemetry.RecordMatch(r, results, matchCount, duration, telemetry.MatchOptions{
		TopHitOnly:             topHitOnly,
		AllowFirstBlockMatches: allowFirstBlockMatches,
		ClassyFireEnabled:      classyfireEnabled,
	})

	csvRequested := (r.Header.Get("Accept") == "text/csv") || (r.URL.Query().Get("format") == "csv")

	// Emit matches immediately, and classifications as they come. Not possible for CSV
	if stream && classyfireEnabled && !csvRequested {
		streamMatchResults(w, r, results)
		return
	}

	// Non-streaming, write full response once ClassyFire is done
	if classyfireEnabled {
		enrichWithClassyFire(r.Context(), results)
	}

	if csvRequested {
		w.Header().Set("Content-Type", "text/csv")
		err := writeResultsAsCSV(w, results, classyfireEnabled)
		if err != nil {
			log.Printf("Failed to write CSV response: %v", err)
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(results)
		if err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			log.Printf("Failed to encode JSON response: %v", err)
		}
	}

}

// streamMatchResults writes the response as NDJSON
func streamMatchResults(w http.ResponseWriter, r *http.Request, results []*model.SingleResult) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		enrichWithClassyFire(r.Context(), results)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(results); err != nil &&
			!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			log.Printf("Failed to encode JSON response: %v", err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	enc := json.NewEncoder(w)
	writeLine := func(v any) bool {
		if err := enc.Encode(v); err != nil {
			if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
				log.Printf("Failed to write stream message: %v", err)
			}
			return false
		}
		flusher.Flush()
		return true
	}

	keys := classifiableKeys(results)

	// Count toward the shared-gate queue depth
	if len(keys) > 0 {
		cfbEnterQueue()
		defer cfbLeaveQueue()
	}

	if !writeLine(map[string]any{"type": "matches", "results": results, "unique": len(keys), "queue": cfbQueueDepth()}) {
		return
	}

	streamClassyFire(r.Context(), keys, func(key string, info *model.ClassyFireInfo) {
		writeLine(map[string]any{"type": "classyfire", "inchikey": key, "info": info, "queue": cfbQueueDepth()})
	})

	writeLine(map[string]any{"type": "done"})
}

func Status(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprintln(w, "The CTSLite server is up and running!")
	if err != nil {
		log.Printf("Status check failed to write response: %v", err)
		return
	}
}
