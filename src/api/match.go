package api

import (
	"ctslite/model"
)

func matchInchi(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compound, ok := index.ByInChI[query]
	if !ok {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}

	result.MatchFound = true
	result.MatchLevel = "Exact InChI"
	result.Matches = []*model.Compound{compound}
}

func matchInchiKey(index *model.PubChemIndex, query string, result *model.SingleResult) {
	// Try full inchikey match first
	compound, ok := index.ByInChIKey[query]
	if ok {
		result.MatchFound = true
		result.MatchLevel = "Exact InChIKey"
		result.Matches = []*model.Compound{compound}
		return
	} else {
		// If full inchikey match failed, try first block
		// The first 14 characters will always be a properly formatted FirstBlock
		queryFirstBlock := query[:14]
		compounds, ok := index.ByFirstBlock[queryFirstBlock]
		if !ok {
			result.MatchFound = false
			result.ErrMsg = "No compound(s) found"
			return
		}
		result.MatchFound = true
		result.MatchLevel = "First Block"
		result.Matches = compounds
	}
}

func matchSmiles(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, ok := index.BySmiles[query]
	if !ok {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}

	for _, compound := range compounds {
		result.Matches = append(result.Matches, compound)
		result.MatchFound = true
		result.MatchLevel = "Exact SMILES"
	}
}

func matchFormula(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, ok := index.ByFormula[query]
	if !ok {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}

	for _, compound := range compounds {
		result.Matches = append(result.Matches, compound)
		result.MatchFound = true
		result.MatchLevel = "Exact Formula"
	}
}

func matchSmilesOrFormula(index *model.PubChemIndex, query string, result *model.SingleResult) {
	compounds, ok := index.ByFormula[query]
	if !ok {
		// If formula match failed, try smiles
		compounds, ok = index.BySmiles[query]
		if !ok {
			result.MatchFound = false
			result.ErrMsg = "No compound found"
			return
		}

		for _, compound := range compounds {
			result.QueryType = "smiles"
			result.Matches = append(result.Matches, compound)
			result.MatchFound = true
			result.MatchLevel = "Exact SMILES"
		}
		return
	}
	
	for _, compound := range compounds {
		result.QueryType = "formula"
		result.Matches = append(result.Matches, compound)
		result.MatchFound = true
		result.MatchLevel = "Exact Formula"
	}
}

