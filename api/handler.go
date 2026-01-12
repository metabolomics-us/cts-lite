package api

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
)

var inchikeyPattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)
var badInchikeyPattern = regexp.MustCompile(`^[a-zA-Z]{12,16}-[a-zA-Z]{9,11}-[a-zA-Z]{0,2}$`)
var smilesPattern = regexp.MustCompile(`^[CB[OFNSPI]$`) // The only first characters of SMILES in all of PubChemLite

func parseQueryType(q string) string {
	switch {
	case inchikeyPattern.MatchString(q):
		log.Println("Query identified as InChIKey")
		return "inchikey"

	case badInchikeyPattern.MatchString(q):
		log.Println("Query identified as malformed InChIKey")
		return "bad_inchikey"

	case strings.HasPrefix(q, "InChI="):
		log.Println("Query identified as InChI")
		return "inchi"

	case strings.HasPrefix(strings.ToLower(q), "inchi="):
		log.Println("Query identified as malformed InChI")
		return "bad_inchi"

	// See if first char matches any first char of SMILES in the db
	case smilesPattern.MatchString(q[0:1]):
		log.Println("Query identified as SMILES")
		return "smiles"

	default:
		log.Println("Query type could not be identified")
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
					match.FirstBlock,
					match.InChI,
					match.Smiles,
					match.CompoundName,
					match.MolecularFormula,
					match.PubMedCount,
					match.PatentCount,
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
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if raw == "" {
		http.Error(w, "Query was empty. Expecting '/match?q=<query(s)>'", http.StatusBadRequest)
		return
	}

	// Split query by space or newline (can't use comma because InChI or SMILES can contain commas)
	splitter := regexp.MustCompile(`[\s]+`)
	queries := splitter.Split(raw, -1)

	results := make([]*model.SingleResult, 0, len(queries))

	for _, q := range queries {
		q = strings.TrimSpace(q)

		if q == "" {
			continue
		}

		if strings.HasPrefix(q, "\"") && strings.HasSuffix(q, "\"") {
			q = q[1 : len(q)-1]
		}

		result := &model.SingleResult{
			Query:     q,
			QueryType: parseQueryType(q),
		}

		switch result.QueryType {
		case "inchi":
			matchInchi(index, q, result)

		case "inchikey":
			matchInchiKey(index, q, result)

		case "smiles":
			matchSmiles(index, q, result)

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
			http.Error(w, "An unexpected error occurred when parsing the request", http.StatusInternalServerError)
			return
		}

		results = append(results, result)
	}

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
	// log.Println("Status check successful") // Was inflating the logs unnecessarily
}
