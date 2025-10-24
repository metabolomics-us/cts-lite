package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	// "encoding/json"
)

type Compound struct {
	inchikey     string `json:"inchikey"`
	firstblock   string `json:"first_block"`
	inchi        string `json:"inchi"`
	smiles       string `json:"smiles"`
	compoundname string `json:"compound_name"`
	formula      string `json:"molecular_formula`
}

type PubChemIndex struct {
	compounds    []*Compound
	byInchikey   map[string]*Compound
	byInchi      map[string]*Compound
	bySmiles     map[string]*Compound
	byFirstblock map[string][]*Compound
}

var inchikey_pattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)

func parseQueryType(q string) string {
	switch {
	case strings.HasPrefix(q, "InChI="):
		return "inchi"

	case inchikey_pattern.MatchString(q):
		return "inchikey"

	default:
		return "smiles"
	}
}

func loadPubChemLite(filepath string) (*PubChemIndex, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header of csv file

	index := &PubChemIndex{
		byInchikey:   make(map[string]*Compound),
		byInchi:      make(map[string]*Compound),
		bySmiles:     make(map[string]*Compound),
		byFirstblock: make(map[string][]*Compound),
	}
}

func status(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "The CTSLite server is up and running!")
}

func matchSmiles(w http.ResponseWriter, r *http.Request) {

}

func matchInchiKey(w http.ResponseWriter, r *http.Request) {

}

func matchInchi(w http.ResponseWriter, r *http.Request) {

}

func main() {
	// Load PubChemLite into memory
	var filePath string = "PubChemLite_CCSbase_20250905.csv"
	fmt.Println("Loading PubChemLite into memory...")
	index, err := loadPubChemLite(filePath)
	if err != nil {
		log.Fatalf("Error reading file: %v", err)
	}
	fmt.Println("Loaded %d compounds", len(index.compounds))

	// Default endpoints for health checks
	http.HandleFunc("/", status)
	http.HandleFunc("/health", status)
	http.HandleFunc("/status", status)

	// Endpoints for matching against PubChemLite
	http.HandleFunc("/smiles", matchSmiles)
	http.HandleFunc("/inchikey", matchInchiKey)
	http.HandleFunc("/inchi", matchInchi)

	fmt.Println("Server launching on :8080")
	http.ListenAndServe(":8080", nil)
}
