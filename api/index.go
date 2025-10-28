package api

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
)

func LoadPubChemLite(filepath string) (*PubChemIndex, error) {
	// Open the file
	f, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open PubChemLite file: %w", err)
	}

	// Close the file (deferred)
	defer func() {
		err := f.Close()
		if err != nil {
			log.Printf("Failed to close PubChemLite file: %v", err)
		}
	}()

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header of csv file

	index := &PubChemIndex{
		byInChIKey:   make(map[string]*Compound),
		byInChI:      make(map[string]*Compound),
		bySmiles:     make(map[string]*Compound),
		byFirstBlock: make(map[string][]*Compound),
	}

	// Loop until we reach EOF
	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("failed to read PubChemLite file: %w", err)
		}

		c := &Compound{
			FirstBlock:       line[1],
			MolecularFormula: line[6],
			Smiles:           line[7],
			InChI:            line[8],
			InChIKey:         line[9],
			CompoundName:     line[12],
		}

		index.Compounds = append(index.Compounds, c)
		index.byInChIKey[c.InChIKey] = c
		index.byInChI[c.InChI] = c
		index.bySmiles[c.Smiles] = c
		index.byFirstBlock[c.FirstBlock] = append(index.byFirstBlock[c.FirstBlock], c)
	}

	return index, nil
}
