package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var inchikeyPattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)
var badInchikeyPattern = regexp.MustCompile(`^[a-zA-Z]{12,16}-[a-zA-Z]{9,11}-[a-zA-Z]{0,2}$`)

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func parseQueryType(q string) string {
	// Order of cases matters here
	switch {
	case inchikeyPattern.MatchString(q):
		// log.Println("Query identified as InChIKey")
		return "inchikey"

	case badInchikeyPattern.MatchString(q):
		// log.Println("Query identified as malformed InChIKey")
		return "bad_inchikey"

	case strings.HasPrefix(q, "InChI="):
		// log.Println("Query identified as InChI")
		return "inchi"

	case len(q) >= 6 && strings.EqualFold(q[:6], "inchi="):
		// log.Println("Query identified as malformed InChI")
		return "bad_inchi"

	// See if first char matches any first char of SMILES in the db
	case strings.ContainsAny(q, "=#/\\:.@+-[]()"):
		// log.Println("Query identified as SMILES")
		return "smiles"

	case strings.ContainsRune("ADEGHKLMRTUVWXYZ", rune(q[0])):
		// log.Println("Query identified as Molecular Formula")
		return "formula"

	case strings.ContainsRune("ABCDEFGHIKLMNOPRSTUVWXYZ", rune(q[0])):
		// log.Println("Query identified as SMILES or Molecular Formula")
		return "smiles_or_formula"

	case isAllDigits(q):
		// log.Println("Query identified as PubChem ID")
		return "pubchem_id"

	default:
		// log.Println("Query type could not be identified")
		return "unidentified"
	}
}

// writeResultsAsCSV converts the results to CSV format and writes to the response writer
func writeResultsAsCSV(w http.ResponseWriter, results []*model.SingleResult, classyfireEnabled bool) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write CSV header
	header := []string{
		"query", "query_type", "found_match", "match_level", "error_message",
		"pubchem_cid", "inchikey", "inchi", "smiles", "compound_name",
		"molecular_formula", "exact_mass", "literature_count", "patent_count",
	}
	if classyfireEnabled {
		header = append(header,
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
				strconv.FormatBool(result.MatchFound),
				result.MatchLevel,
				result.ErrMsg,
				"", "", "", "", "", "", "", "", "", // Empty compound fields
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
	if classyfireEnabled && len(queries) > 100 {
		http.Error(w, fmt.Sprintf("Query contains %d identifiers (limit 100 when ClassyFire is enabled)", len(queries)), http.StatusBadRequest)
		return
	}

	results := make([]*model.SingleResult, 0, len(queries))
	var matchCount int = 0
	timeStart := time.Now()

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
			matchSmiles(index, q, result, topHitOnly)

		case "formula":
			matchFormula(index, q, result, topHitOnly)

		case "smiles_or_formula":
			matchSmilesOrFormula(index, q, result, topHitOnly)

		case "bad_inchi":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChI, see documentation"

		case "bad_inchikey":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChIKey, see documentation"

		case "unidentified":
			result.MatchFound = false
			result.ErrMsg = "Invalid query type, could not identify, see documentation"

		default:
			log.Printf("ERROR: An unexpected error occured when parsing the request. Query type unhandled. Query: '%s'", q)
			http.Error(w, "An unexpected error occurred when parsing the request", http.StatusInternalServerError)
			return
		}

		if result.MatchFound {
			matchCount++
		}

		results = append(results, result)
	}

	log.Printf("%d matches found from %d queries in %s\n", matchCount, len(queries), time.Since(timeStart).Round(time.Millisecond))

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
