package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"ctslite/data"
)

func matchInchi(index *data.PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.ByInChI[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided InChI: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*data.Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func matchInchiKey(index *data.PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.ByInChIKey[query]
	if ok {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode([]*data.Compound{compound})
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
		return
	} else {
		// Try to match first block if full match failed
		// We already trimmed query and checked for the inchikey pattern earlier
		// The first 14 characters will always be a properly formatted FirstBlock
		queryFirstBlock := query[:14]
		compounds, ok := index.ByFirstBlock[queryFirstBlock]
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

func matchSmiles(index *data.PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.BySmiles[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided SMILES: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*data.Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
