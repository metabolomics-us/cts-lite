package api

import (
	"ctslite/model"
	"ctslite/rdkit"
	"ctslite/synonyms"
	"log"
)

var smilesToInChIKey = func(smiles string) (string, error) {
	return rdkit.SmilesToInChIKey(smiles)
}

var inchiToInChIKey = func(inchi string) (string, error) {
	return rdkit.InChIToInChIKey(inchi)
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
		result.ErrMsg = "RDKit conversion failed"
		result.MatchFound = false
		return
	}
	// Keep the converted inchikey in case pubchem fallback needs it later
	result.DerivedInChIKey = inchikey

	if inchikey != "" {
		matchInchiKey(index, inchikey, result, allowFirstBlockMatches, topHitOnly)
		if result.MatchFound {
			result.QueryType = "converted_smiles"
			result.ConvertedQuery = inchikey
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

func matchCAS(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, err := index.QueryByCAS(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by CAS: %v", err)
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
	result.MatchLevel = "Exact CAS"
	result.Matches = compounds
}

// matchHMDB looks the ID up in the 7-digit form (i.e. HMDB01847 finds HMDB0001847)
func matchHMDB(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	id, _ := synonyms.NormalizeHMDB(query)
	compounds, err := index.QueryByHMDB(id, topHitOnly)
	if err != nil {
		log.Printf("Error querying by HMDB ID: %v", err)
		result.MatchFound = false
		result.ErrMsg = "Internal server error"
		return
	}
	// The padded ID shows what a short or lowercase query was read as
	if id != query {
		result.ConvertedQuery = id
	}
	if len(compounds) == 0 {
		result.MatchFound = false
		result.ErrMsg = "No compound found"
		return
	}
	result.MatchFound = true
	result.MatchLevel = "Exact HMDB"
	result.Matches = compounds
}

// nameFallbackTypes are the query types that try a name lookup once their own lookup missed
var nameFallbackTypes = map[string]bool{
	"smiles": true, "formula": true, "smiles_or_formula": true, "unidentified": true,
}

// matchName looks the query up as a compound name.
// A miss leaves the result as it was, so a structure query still reaches the PubChem
// fallback with its own error
func matchName(index *model.PubChemIndex, query string, result *model.SingleResult, topHitOnly bool) {
	compounds, key, err := index.QueryByNameKey(query, topHitOnly)
	if err != nil {
		log.Printf("Error querying by name: %v", err)
		return
	}
	if len(compounds) == 0 {
		return
	}
	result.MatchFound = true
	result.QueryType = "name"
	result.ErrMsg = ""
	result.MatchLevel = "Exact Name"
	// A variant is a rewrite of the query, so it shows what the query was read as
	if key != synonyms.Normalize(query) {
		result.MatchLevel = "Name Variant"
		result.ConvertedQuery = key
	}
	result.Matches = compounds
}

var forcedQueryTypes = map[string]struct {
	label   string
	accepts map[string]bool
}{
	"pubchem_id": {"PubChem CID", map[string]bool{"pubchem_id": true}},
	"inchikey":   {"InChIKey", map[string]bool{"inchikey": true}},
	"inchi":      {"InChI", map[string]bool{"inchi": true}},
	"smiles":     {"SMILES", map[string]bool{"smiles": true, "smiles_or_formula": true}},
	"formula":    {"molecular formula", map[string]bool{"formula": true, "smiles_or_formula": true}},
	"cas":        {"CAS ID", map[string]bool{"cas": true}},
	"hmdb":       {"HMDB ID", map[string]bool{"hmdb": true}},
	"name":       {"name", nil}, // name could look like any type
}

// notNameTypes can never be stored as names (for parsing purposes)
var notNameTypes = map[string]bool{
	"pubchem_id": true, "bad_pubchem_id": true, "inchi": true, "bad_inchi": true,
	"inchikey": true, "bad_inchikey": true, "cas": true, "bad_cas": true, "hmdb": true, "bad_hmdb": true,
}

// matchForced runs only the lookup of the forced query type, unless the type is incompatible
// (i.e. CID detected query trying to be forced as SMILES does not run anything)
func matchForced(index *model.PubChemIndex, query string, result *model.SingleResult, forced string, allowFirstBlockMatches bool, topHitOnly bool) {
	detected := result.QueryType
	result.QueryType = forced
	accepted := forcedQueryTypes[forced].accepts[detected]
	if forced == "name" {
		accepted = !notNameTypes[detected]
	}
	if !accepted {
		result.Rejected = true
		result.ErrMsg = "Not a valid " + forcedQueryTypes[forced].label
		return
	}

	switch forced {
	case "pubchem_id":
		matchPubChemID(index, query, result, topHitOnly)
	case "inchikey":
		matchInchiKey(index, query, result, allowFirstBlockMatches, topHitOnly)
	case "inchi":
		matchInchi(index, query, result, topHitOnly)
	case "smiles":
		matchSmiles(index, query, result, allowFirstBlockMatches, topHitOnly)
	case "formula":
		matchFormula(index, query, result, topHitOnly)
	case "cas":
		matchCAS(index, query, result, topHitOnly)
	case "hmdb":
		matchHMDB(index, query, result, topHitOnly)
	case "name":
		// A name miss leaves the result untouched, so it needs its own error
		matchName(index, query, result, topHitOnly)
		if !result.MatchFound {
			result.ErrMsg = "No compound found"
		}
	}
}

// addForcedNote marks a miss as coming from a forced query type. Internal errors stay
// as they are since the PubChem fallback checks for them by value
func addForcedNote(result *model.SingleResult, forced string) {
	if forced == "" || result.MatchFound || result.ErrMsg == "" || result.ErrMsg == "Internal server error" {
		return
	}
	result.ErrMsg += " (forced query type: " + forced + ")"
}

func matchSmilesOrFormula(index *model.PubChemIndex, query string, result *model.SingleResult, allowFirstBlockMatches bool, topHitOnly bool) {
	matchSmiles(index, query, result, allowFirstBlockMatches, topHitOnly)
	if result.MatchFound {
		if result.QueryType != "converted_smiles" {
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
