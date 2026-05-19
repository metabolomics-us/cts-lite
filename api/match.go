package api

import (
	"ctslite/model"
	"ctslite/rdkit"
	"log"
)

var smilesToInChIKey = func(smiles string) (string, error) {
	return rdkit.SmilesToInChIKey(smiles)
}

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

func matchSmiles(index *model.PubChemIndex, query string, result *model.SingleResult, allowFirstBlockMatches bool, topHitOnly bool) {
	compounds, err := index.QueryBySmiles(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by SMILES: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	if len(compounds) > 0 {
		result.MatchFound = true
		result.MatchLevel = "Exact SMILES"
		result.Matches = compounds
		return
	}
	// Check for overly long SMILES, to avoid passing them to RDKit. 4096 is invalid anyway
	if len(query) > 4096 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}

	inchikey, err := smilesToInChIKey(query)
	if err != nil {
		log.Printf("RDKit InChIKey conversion failed for %q: %v", query, err)
	}
	if inchikey != "" {
		matchInchiKey(index, inchikey, result, allowFirstBlockMatches, topHitOnly)
		if result.MatchFound {
			result.QueryType = "translated_smiles"
			result.TranslatedQuery = inchikey
			return
		}
	}

	result.MatchFound = false
	result.ErrMsg = "No compound found"
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

func matchSmilesOrFormula(index *model.PubChemIndex, query string, result *model.SingleResult, allowFirstBlockMatches bool, topHitOnly bool) {
	matchSmiles(index, query, result, allowFirstBlockMatches, topHitOnly)
	if result.MatchFound {
		if result.QueryType != "translated_smiles" {
			result.QueryType = "smiles"
		}
		return
	}
	if result.ErrMsg == "Internal server error" {
		return
	}

	result.ErrMsg = ""
	matchFormula(index, query, result, topHitOnly)
	if result.MatchFound {
		result.QueryType = "formula"
	}
}
