package api

import (
	"ctslite/model"
	"log"
)

func matchInchi(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, err := index.QueryByInChI(query)
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

func matchInchiKey(index *model.PubChemIndex, query string, result *model.SingleResult) {
	// Try full InChIKey match first
	compounds, err := index.QueryByInChIKey(query)
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
	compounds, err = index.QueryByFirstBlock(query[:14])
	if err != nil {
		log.Printf("Error querying by first block: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound(s) found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "First Block"
	result.Matches = compounds
}

func matchSmiles(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, err := index.QueryBySmiles(query)
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

func matchFormula(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, err := index.QueryByFormula(query)
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

func matchSmilesOrFormula(index *model.PubChemIndex, query string, result *model.SingleResult) {
	// Try formula first
	compounds, err := index.QueryByFormula(query)
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
	compounds, err = index.QueryBySmiles(query)
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
