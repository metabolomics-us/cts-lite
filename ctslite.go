package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

type Compound struct {
	InChIKey         string `json:"inchikey"`
	FirstBlock       string `json:"first_block"`
	InChI            string `json:"inchi"`
	Smiles           string `json:"smiles"`
	CompoundName     string `json:"compound_name"`
	MolecularFormula string `json:"molecular_formula"`
}

type PubChemIndex struct {
	compounds    []*Compound
	byInChIKey   map[string]*Compound
	byInChI      map[string]*Compound
	bySmiles     map[string]*Compound
	byFirstBlock map[string][]*Compound
}

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

func loadPubChemLite(filepath string) (*PubChemIndex, error) {
	// Open the file
	f, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open PubChemLite file: %w", err)
	}

	// Close the file (deferred)
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("Failed to close PubChemLite file: %v", err)
		}
	}()

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header of csv file

	index := &PubChemIndex{
		byInChIKey:   make(map[string]*Compound),
		byInChI:      make(map[string]*Compound),
		bySmiles:     make(map[string]*Compound),
		byFirstBlock: make(map[string][]*Compound),
	}

	// Loop until we reach EOF
	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("failed to read PubChemLite file: %w", err)
		}

		c := &Compound{
			FirstBlock:       line[1],
			MolecularFormula: line[6],
			Smiles:           line[7],
			InChI:            line[8],
			InChIKey:         line[9],
			CompoundName:     line[12],
		}

		index.compounds = append(index.compounds, c)
		index.byInChIKey[c.InChIKey] = c
		index.byInChI[c.InChI] = c
		index.bySmiles[c.Smiles] = c
		index.byFirstBlock[c.FirstBlock] = append(index.byFirstBlock[c.FirstBlock], c)
	}

	return index, nil
}

func matchInchi(index *PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.byInChI[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided InChI: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func matchInchiKey(index *PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.byInChIKey[query]
	if ok {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode([]*Compound{compound})
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
		return
	} else {
		// Try to match first block if full match failed
		// We already trimmed query and checked for the inchikey pattern earlier
		// The first 14 characters will always be a properly formatted FirstBlock
		queryFirstBlock := query[:14]
		compounds, ok := index.byFirstBlock[queryFirstBlock]
		if !ok {
			err := fmt.Errorf("no compound(s) found for provided InChIKey %s", query)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(compounds)
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
	}
}

func matchSmiles(index *PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.bySmiles[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided SMILES: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func match(index *PubChemIndex, w http.ResponseWriter, r *http.Request) {
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

func status(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprintln(w, "The CTSLite server is up and running!")
	if err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}

func main() {
	// Load PubChemLite into memory
	filePath := "PubChemLite_CCSbase_20250905.csv"
	fmt.Printf("Loading PubChemLite into memory using %v...\n", filePath)
	index, err := loadPubChemLite(filePath)
	if err != nil {
		log.Fatalf("Error loading PubChemLite: %v. Tried using the following file: %v", err, filePath)
	}
	fmt.Printf("Loaded %d compounds\n", len(index.compounds))

	// Default endpoints for health checks
	http.HandleFunc("/", status)
	http.HandleFunc("/health", status)
	http.HandleFunc("/status", status)

	// Endpoints for matching against PubChemLite
	http.HandleFunc("/match", func(w http.ResponseWriter, r *http.Request) {
		match(index, w, r)
	})

	fmt.Println("Server launching on port 8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Error starting server: %v", err)
		return
	}
}
