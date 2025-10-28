package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

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
