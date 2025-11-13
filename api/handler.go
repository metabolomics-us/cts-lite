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

func parseQueryType(q string) string {
	switch {
	case strings.HasPrefix(q, "InChI="):
		log.Println("Query identified as InChI")
		return "inchi"

	case inchikeyPattern.MatchString(q):
		log.Println("Query identified as InChIKey")
		return "inchikey"

	default:
		log.Println("Query identified as SMILES")
		return "smiles"
	}
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
		log.Printf("Failed to write response: %v", err)
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
