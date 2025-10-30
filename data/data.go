package data

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
)

type Compound struct {
	InChIKey         string `json:"inchikey"`
	FirstBlock       string `json:"first_block"`
	InChI            string `json:"inchi"`
	Smiles           string `json:"smiles"`
	CompoundName     string `json:"compound_name"`
	MolecularFormula string `json:"molecular_formula"`
	PubMedCount      string `json:"pubmed_count"`
	PatentCount      string `json:"patent_count"`
}

type PubChemIndex struct {
	Compounds    []*Compound
	ByInChIKey   map[string]*Compound
	ByInChI      map[string]*Compound
	BySmiles     map[string]*Compound
	ByFirstBlock map[string][]*Compound
}

func LoadPubChemLite(file string) (*PubChemIndex, error) {
	// Open the file
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	// Close the file (deferred)
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header of csv file

	index := &PubChemIndex{
		ByInChIKey:   make(map[string]*Compound),
		ByInChI:      make(map[string]*Compound),
		BySmiles:     make(map[string]*Compound),
		ByFirstBlock: make(map[string][]*Compound),
	}

	fmt.Printf("Loading PubChemLite into memory using %v...\n", file)

	// Loop until we reach EOF
	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}

		c := &Compound{
			FirstBlock:       line[1],
			MolecularFormula: line[6],
			Smiles:           line[7],
			InChI:            line[8],
			InChIKey:         line[9],
			CompoundName:     line[12],
			PubMedCount:      line[2],
			PatentCount:      line[3],
		}

		index.Compounds = append(index.Compounds, c)
		index.ByInChIKey[c.InChIKey] = c
		index.ByInChI[c.InChI] = c
		index.BySmiles[c.Smiles] = c
		index.ByFirstBlock[c.FirstBlock] = append(index.ByFirstBlock[c.FirstBlock], c)
	}

	return index, nil
}
