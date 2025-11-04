package api

import (
	"ctslite/model"
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
// It detects the type of query and delegates it to the corresponding matching function
func Match(index *model.PubChemIndex, w http.ResponseWriter, r *http.Request) {
	// Extract query from request
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "Incorrect query format. Expecting '/match?q=<query>'", http.StatusBadRequest)
		return
	}

	queryType := parseQueryType(q)

	switch queryType {
	case "inchi":
		matchInchi(index, w, q)
	case "inchikey":
		matchInchiKey(index, w, q)
	case "smiles":
		matchSmiles(index, w, q)
	default:
		http.Error(w, "An unexpected error occurred when parsing the request", http.StatusInternalServerError)
	}
}

func Status(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprintln(w, "The CTSLite server is up and running!")
	if err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}
