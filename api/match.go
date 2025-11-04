package api

import (
	"ctslite/model"
	"encoding/json"
	"fmt"
	"net/http"
)

func matchInchi(index *model.PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.ByInChI[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided InChI: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*model.Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func matchInchiKey(index *model.PubChemIndex, w http.ResponseWriter, query string) {
	// Try full inchikey match first
	compound, ok := index.ByInChIKey[query]
	if ok {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode([]*model.Compound{compound})
		if err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
		return
	} else {
		// If full inchikey match failed, try first block
		// We already trimmed query and checked for the inchikey pattern earlier
		// The first 14 characters will always be a properly formatted FirstBlock
		queryFirstBlock := query[:14]
		compounds, ok := index.ByFirstBlock[queryFirstBlock]
		if !ok {
			err := fmt.Errorf("no compound found for provided InChIKey %s. No first block matches found either", query)
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

func matchSmiles(index *model.PubChemIndex, w http.ResponseWriter, query string) {
	compound, ok := index.BySmiles[query]
	if !ok {
		err := fmt.Errorf("no compound found for provided SMILES: %s", query)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode([]*model.Compound{compound})
	if err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
