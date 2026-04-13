package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var inchikeyPattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)
var badInchikeyPattern = regexp.MustCompile(`^[a-zA-Z]{12,16}-[a-zA-Z]{9,11}-[a-zA-Z]{0,2}$`)
var smilesGuaranteePattern = regexp.MustCompile(`[=#\/\\:\.@+\-\[\]\(\)]`)
var formulaGuaranteePattern = regexp.MustCompile(`^[ADEGHKLMRTUVWXYZ]$`) // Characters that cannot be found at the start of smiles
var smilesOrFormulaPattern = regexp.MustCompile(`^[ABCDEFGHIKLMNOPRSTUVWXYZ]$`) // Characters that can start both smiles and formulas
var pubchemIDPattern = regexp.MustCompile(`^[0-9]+$`) // Only numbers

func parseQueryType(q string) string {
	// Order of cases matters here
	switch {
	case pubchemIDPattern.MatchString(q):
		// log.Println("Query identified as PubChem ID")
		return "pubchem_id"

	case inchikeyPattern.MatchString(q):
		// log.Println("Query identified as InChIKey")
		return "inchikey"

	case badInchikeyPattern.MatchString(q):
		// log.Println("Query identified as malformed InChIKey")
		return "bad_inchikey"

	case strings.HasPrefix(q, "InChI="):
		// log.Println("Query identified as InChI")
		return "inchi"

	case strings.HasPrefix(strings.ToLower(q), "inchi="):
		// log.Println("Query identified as malformed InChI")
		return "bad_inchi"

	// See if first char matches any first char of SMILES in the db
	case smilesGuaranteePattern.MatchString(q):
		// log.Println("Query identified as SMILES")
		return "smiles"

	case formulaGuaranteePattern.MatchString(q[0:1]):
		// log.Println("Query identified as Molecular Formula")
		return "formula"

	case smilesOrFormulaPattern.MatchString(q[0:1]):
		// log.Println("Query identified as SMILES or Molecular Formula")
		return "smiles_or_formula"

	default:
		// log.Println("Query type could not be identified")
		return "unidentified"
	}
}

// writeResultsAsCSV converts the results to CSV format and writes to the response writer
func writeResultsAsCSV(w http.ResponseWriter, results []*model.SingleResult) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write CSV header
	header := []string{
		"query", "query_type", "found_match", "match_level", "error_message",
		"inchikey", "first_block", "inchi", "smiles", "compound_name",
		"molecular_formula", "pubmed_count", "patent_count",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write data rows
	for _, result := range results {
		if !result.MatchFound {
			// Write a single row for failed matches
			row := []string{
				result.Query,
				result.QueryType,
				fmt.Sprintf("%t", result.MatchFound),
				result.MatchLevel,
				result.ErrMsg,
				"", "", "", "", "", "", "", "", // Empty compound fields
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
					fmt.Sprintf("%t", result.MatchFound),
					result.MatchLevel,
					result.ErrMsg,
					match.InChIKey,
					match.InChIKey[:14], // firstblock
					match.InChI,
					match.Smiles,
					match.CompoundName,
					match.MolecularFormula,
					strconv.FormatFloat(float64(match.PubMedCount), 'f', -1, 32),
					strconv.FormatFloat(float64(match.PatentCount), 'f', -1, 32),
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

	// Split query by space or newline (can't use comma because InChI or SMILES can contain commas)
	splitter := regexp.MustCompile(`[\s]+`)
	queries := splitter.Split(rawQuery, -1)

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
			matchPubChemID(index, q, result)

		case "inchi":
			matchInchi(index, q, result)

		case "inchikey":
			matchInchiKey(index, q, result)

		case "smiles":
			matchSmiles(index, q, result)

		case "formula":
			matchFormula(index, q, result)

		case "smiles_or_formula":
			matchSmilesOrFormula(index, q, result)

		case "bad_inchi":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChI, see documentation"

		case "bad_inchikey":
			result.MatchFound = false
			result.ErrMsg = "Malformed InChIKey, see documentation"

		case "unidentified":
			result.MatchFound = false
			result.ErrMsg = "Invalid query type, could not identify"

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

	log.Printf("%d matches found from %d queries in %f seconds\n", matchCount, len(queries), time.Since(timeStart).Seconds())

	// Check for header text/csv and respond accordingly
	if (r.Header.Get("Accept") == "text/csv") || (r.URL.Query().Get("format") == "csv") {
		// Respond with CSV
		w.Header().Set("Content-Type", "text/csv")
		err := writeResultsAsCSV(w, results)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to write CSV response: %v", err), http.StatusInternalServerError)
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(results)
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
	}

}

func Status(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprintln(w, "The CTSLite server is up and running!")
	if err != nil {
		log.Printf("Status check failed to write response: %v", err)
		return
	}
}

