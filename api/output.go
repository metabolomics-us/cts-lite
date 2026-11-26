package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"ctslite/model"
)

var CSVHeader = []string{
	"query", "query_type", "converted_query", "found_match", "match_level", "error_message",
	"pubchem_cid", "inchikey", "inchi", "smiles", "compound_name",
	"molecular_formula", "exact_mass", "literature_count", "patent_count", "annotation_type_count",
	"cas_ids", "hmdb_ids",
}

// used to join a compound's CAS or HMDB IDs into one CSV cell
const csvIDSeparator = ";"

// writeResultsAsCSV converts the results to CSV format and writes to the response writer
func writeResultsAsCSV(w http.ResponseWriter, results []*model.SingleResult, classyfireEnabled bool) error {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write CSV header
	header := CSVHeader
	if classyfireEnabled {
		// Clip forces append to copy rather than mutate the shared CSVHeader
		header = append(slices.Clip(header),
			"classyfire_kingdom", "classyfire_superclass", "classyfire_class",
			"classyfire_subclass", "classyfire_direct_parent", "classyfire_description",
			"classyfire_error",
		)
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	cfFields := func(cf *model.ClassyFireInfo) []string {
		if cf == nil {
			return []string{"", "", "", "", "", "", ""}
		}
		return []string{cf.Kingdom, cf.Superclass, cf.Class, cf.Subclass, cf.DirectParent, cf.Description, cf.Error}
	}

	// Write data rows
	for _, result := range results {
		if !result.MatchFound {
			// Write one row per failed match
			row := []string{
				result.Query,
				result.QueryType,
				result.ConvertedQuery,
				strconv.FormatBool(result.MatchFound),
				result.MatchLevel,
				result.ErrMsg,
				"", "", "", "", "", "", "", "", "", "", "", "", // Empty compound fields
			}
			if classyfireEnabled {
				row = append(row, cfFields(nil)...)
			}
			if err := writer.Write(row); err != nil {
				return fmt.Errorf("failed to write CSV row: %w", err)
			}
		} else {
			// Write one row per match
			for _, match := range result.Matches {
				row := []string{
					result.Query,
					result.QueryType,
					result.ConvertedQuery,
					strconv.FormatBool(result.MatchFound),
					result.MatchLevel,
					result.ErrMsg,
					match.Identifier,
					match.InChIKey,
					match.InChI,
					match.Smiles,
					match.CompoundName,
					match.MolecularFormula,
					strconv.FormatFloat(match.ExactMass, 'f', -1, 64),
					strconv.FormatFloat(float64(match.LiteratureCount), 'f', -1, 32),
					strconv.FormatFloat(float64(match.PatentCount), 'f', -1, 32),
					strconv.FormatFloat(float64(match.AnnotationTypeCount), 'f', -1, 32),
					strings.Join(match.CASIDs, csvIDSeparator),
					strings.Join(match.HMDBIDs, csvIDSeparator),
				}
				if classyfireEnabled {
					row = append(row, cfFields(match.ClassyFire)...)
				}
				if err := writer.Write(row); err != nil {
					return fmt.Errorf("failed to write CSV row: %w", err)
				}
			}
		}
	}

	return nil
}
