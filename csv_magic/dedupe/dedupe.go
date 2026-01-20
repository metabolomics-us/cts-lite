package main

import (
	"encoding/csv"
	"fmt"
	"os"
)

func main() {
	// Open the CSV received as argument
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run dedupe.go <input.csv>")
		return
	}

	// Open the input CSV
	inputPath := os.Args[1]
	csvFile, err := os.Open(inputPath)
	if err != nil {
		panic(err)
	}
	defer csvFile.Close()

	reader := csv.NewReader(csvFile)
	records, err := reader.ReadAll()
	if err != nil {
		panic(err)
	}

	// Read through all rows and check for duplicate Identifiers
	seen := make(map[string]bool)
	var duplicateCount int = 0
	output := [][]string{records[0]} // Start with header
	for _, row := range records[1:] {
		identifier := row[0]
		inchikey := row[7]
		if seen[identifier] == true || seen[inchikey] == true {
			duplicateCount++
			// Ignore line for new output
			continue
		}
		seen[identifier] = true
		seen[inchikey] = true
		output = append(output, row)
	}

	// Write out to a new CSV without duplicates
	outFile, err := os.Create("deduped_" + inputPath)
	if err != nil {
		panic(err)
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	for _, row := range output {
		if row != nil {
			err := writer.Write(row)
			if err != nil {
				panic(err)
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		panic(err)
	}

	fmt.Printf("Found and removed %d duplicate identifiers in %s.\nFile without dupes stored as deduped_%s\n", duplicateCount, inputPath, inputPath)
}
