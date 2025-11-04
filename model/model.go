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
			FirstBlock:       line[0],
			PubMedCount:      line[1],
			PatentCount:      line[2],
			MolecularFormula: line[3],
			Smiles:           line[4],
			InChI:            line[5],
			InChIKey:         line[6],
			CompoundName:     line[7],
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
