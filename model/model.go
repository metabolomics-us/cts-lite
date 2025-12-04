package model

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

type Compound struct {
	Identifier       string `json:"identifier"`
	InChIKey         string `json:"inchikey"`
	FirstBlock       string `json:"first_block"`
	InChI            string `json:"inchi"`
	Smiles           string `json:"smiles"`
	CompoundName     string `json:"compound_name"`
	MolecularFormula string `json:"molecular_formula"`
	MonoisotopicMass string `json:"monoisotopic_mass"`
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
	startTime := time.Now()
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

		// This only works on the PubChemLite dataset after running the `pubchemlite_trimmer.sh` script
		c := &Compound{
			Identifier:       line[0],
			FirstBlock:       line[1],
			PubMedCount:      line[2],
			PatentCount:      line[3],
			MolecularFormula: line[4],
			Smiles:           line[5],
			InChI:            line[6],
			InChIKey:         line[7],
			MonoisotopicMass: line[8],
			CompoundName:     line[9],
		}

		index.Compounds = append(index.Compounds, c)
		index.ByInChIKey[c.InChIKey] = c
		index.ByInChI[c.InChI] = c
		index.BySmiles[c.Smiles] = c
		index.ByFirstBlock[c.FirstBlock] = append(index.ByFirstBlock[c.FirstBlock], c)
	}

	timeToLoad := time.Since(startTime).Seconds()
	fmt.Printf("Loaded %d compounds, took %.2f seconds\n", len(index.Compounds), timeToLoad)
	return index, nil
}
