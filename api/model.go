package api

type Compound struct {
	InChIKey         string `json:"inchikey"`
	FirstBlock       string `json:"first_block"`
	InChI            string `json:"inchi"`
	Smiles           string `json:"smiles"`
	CompoundName     string `json:"compound_name"`
	MolecularFormula string `json:"molecular_formula"`
}

type PubChemIndex struct {
	Compounds    []*Compound
	byInChIKey   map[string]*Compound
	byInChI      map[string]*Compound
	bySmiles     map[string]*Compound
	byFirstBlock map[string][]*Compound
}
