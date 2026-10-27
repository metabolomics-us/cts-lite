package api

import "testing"

// Polycyclic cage using two-digit %nn ring closure labels
const cageSmiles = "C1C2CC3CC4CC5CC6CC7CC8CC9CC%10CC1C1C2C3C4C5C6C7C8C9C%101"

func TestParseQueryTypeClassification(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"formula_glucose", "C6H12O6", "formula"},
		{"formula_caffeine", "C8H10N4O2", "formula"},
		{"formula_aspirin", "C9H8O4", "formula"},
		{"formula_sucrose", "C12H22O11", "formula"},
		{"formula_water", "H2O", "formula"},
		{"formula_ammonia", "NH3", "formula"},
		{"formula_methane", "CH4", "formula"},
		{"formula_carbon_dioxide", "CO2", "formula"},
		{"formula_fullerene", "C60", "formula"},
		{"formula_dinitrogen_tetroxide", "N2O4", "formula"},
		{"formula_salt", "NaCl", "formula"},
		{"formula_pyrite", "FeS2", "formula"},
		{"formula_silicon_carbide", "SiC", "formula"},
		{"formula_permanganate", "KMnO4", "formula"},
		{"condensed_acetic_acid", "CH3COOH", "formula"},
		{"condensed_ethanol", "CH3CH2OH", "formula"},
		{"condensed_oxalic_acid", "HOOCCOOH", "formula"},

		{"smiles_ethanol", "CCO", "smiles"},
		{"smiles_ethane", "CC", "smiles"},
		{"smiles_hexane", "CCCCCC", "smiles"},
		{"smiles_ethylamine", "CCN", "smiles"},
		{"smiles_ethanol_reversed", "OCC", "smiles"},
		{"smiles_cyclohexane", "C1CCCCC1", "smiles"},
		{"smiles_cage_percent_labels", cageSmiles, "smiles"},
		{"smiles_cage_single_labels", "C1C2CC3CC1CC2C3", "smiles"},
		{"smiles_cubane_skeleton", "C1C2C3C1C1C2C3C1", "smiles"},
		{"smiles_epoxide", "C1CO1", "smiles"},

		{"smiles_oxaziridine_ring", "C1NO1", "smiles_or_formula"},
		{"smiles_oxathiirane_ring", "C1OS1", "smiles_or_formula"},
		{"smiles_thiaziridine_ring", "C1NS1", "smiles_or_formula"},

		{"smiles_benzene_aromatic", "c1ccccc1", "smiles"},
		{"smiles_pyridine_aromatic", "c1ccncc1", "smiles"},
		{"smiles_aniline", "Nc1ccccc1", "smiles"},

		{"ambiguous_carbon", "C", "smiles_or_formula"},
		{"ambiguous_nitrogen", "N", "smiles_or_formula"},
		{"ambiguous_carbon_monoxide", "CO", "smiles_or_formula"},
		{"ambiguous_nitric_oxide", "NO", "smiles_or_formula"},
		{"ambiguous_boron_nitride", "BN", "smiles_or_formula"},
		{"ambiguous_chloromethane", "CCl", "smiles_or_formula"},
		{"ambiguous_paired_digits", "N2O2", "smiles_or_formula"},

		{"junk_formula_typo", "C322cc", "smiles"},

		{"junk_trailing_nonelement", "C6H12O6Q", "unidentified"},
		{"junk_not_an_element", "Xyz", "unidentified"},
		{"junk_word", "hello", "unidentified"},
		{"junk_digits_then_letter", "12345a", "unidentified"},

		{"punctuated_acetic_acid", "CC(=O)O", "smiles"},
		{"punctuated_benzene", "C1=CC=CC=C1", "smiles"},
		{"punctuated_aspirin", "CC(=O)Oc1ccccc1C(=O)O", "smiles"},
		{"punctuated_stereochemistry", "CC[C@H](C)O", "smiles"},
		{"punctuated_formaldehyde", "C=O", "smiles"},

		{"other_inchi", "InChI=1S/H2O/h1H2", "inchi"},
		{"other_bad_inchi", "inchi=1S/H2O/h1H2", "bad_inchi"},
		{"other_inchikey", "MYFAKEINCHIKEY-ISRIGHTHER-E", "inchikey"},
		{"other_bad_inchikey", "myfakeinchikey-isrighther-e", "bad_inchikey"},
		{"other_pubchem_id", "2244", "pubchem_id"},

		// PubChem rejects a whole batch over one CID it cannot accept, so these stop here
		{"pubchem_id_lowest", "1", "pubchem_id"},
		{"pubchem_id_highest", "500000000", "pubchem_id"},
		{"bad_pubchem_id_zero", "0", "bad_pubchem_id"},
		{"bad_pubchem_id_zeros", "000", "bad_pubchem_id"},
		{"bad_pubchem_id_leading_zero", "00702", "bad_pubchem_id"},
		{"bad_pubchem_id_over_max", "500000001", "bad_pubchem_id"},
		{"bad_pubchem_id_overflow", "99999999999999999999999", "bad_pubchem_id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseQueryType(tc.query); got != tc.want {
				t.Errorf("parseQueryType(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}
