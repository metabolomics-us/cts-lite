package api

import (
	"ctslite/model"
	"log"
)

func matchPubChemID(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, err := index.QueryByPubChemID(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by PubChem ID: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "Exact PubChem ID"
	result.Matches = compounds
}

func matchInchi(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, err := index.QueryByInChI(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by InChI: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "Exact InChI"
	result.Matches = compounds
}

func matchInchiKey(index *model.PubChemIndex, query string, result *model.SingleResult, allowFirstBlockMatches bool, topHitOnly bool) {
	// Try full InChIKey match first
	compounds, err := index.QueryByInChIKey(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by InChIKey: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) > 0 {
		result.MatchFound = true
		result.MatchLevel = "Exact InChIKey"
		result.Matches = compounds
		return
	}

	// Fall back to first-block match (first 14 characters of InChIKey)
	if allowFirstBlockMatches {
		compounds, err = index.QueryByFirstBlock(query[:14], topHitOnly)
		if err != nil {
			log.Printf("Error querying by first block: %v", err)
			result.MatchFound = false
			result.ErrMsg = "Internal server error"
			return
		}
		if len(compounds) == 0 {
			result.MatchFound = false
			result.ErrMsg = "No compound found"
			return
		}
		result.MatchFound = true
		result.MatchLevel = "First Block"
		result.Matches = compounds
	} else {
		result.MatchFound = false
		result.ErrMsg = "No compound found, first block matches disabled"
		return
	}
}

func matchSmiles(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, err := index.QueryBySmiles(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by SMILES: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "Exact SMILES"
	result.Matches = compounds
}

func matchFormula(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, err := index.QueryByFormula(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by formula: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "Exact Formula"
	result.Matches = compounds
}

func matchSmilesOrFormula(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	// Try formula first
	compounds, err := index.QueryByFormula(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by formula: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) > 0 {
		result.QueryType = "formula"
		result.MatchFound = true
		result.MatchLevel = "Exact Formula"
		result.Matches = compounds
		return
	}

	// Fall back to SMILES
	compounds, err = index.QueryBySmiles(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by SMILES: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.QueryType = "smiles"
	result.MatchFound = true
	result.MatchLevel = "Exact SMILES"
	result.Matches = compounds
}
