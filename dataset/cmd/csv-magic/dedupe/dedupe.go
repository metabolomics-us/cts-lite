package main

import (
	"encoding/csv"
	"fmt"
	"io"
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

	// Write out to a new CSV without duplicates
	outFile, err := os.Create("deduped_" + inputPath)
	if err != nil {
		panic(err)
	}
	defer outFile.Close()

	reader := csv.NewReader(csvFile)
	writer := csv.NewWriter(outFile)

	// Start with header
	header, err := reader.Read()
	if err != nil {
		panic(err)
	}
	if err := writer.Write(header); err != nil {
		panic(err)
	}

	// Stream through all rows and check for duplicate Identifiers
	seen := make(map[string]bool)
	var duplicateCount int = 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		identifier := row[0]
		if seen[identifier] == true {
			duplicateCount++
			continue
		}
		seen[identifier] = true
		if err := writer.Write(row); err != nil {
			panic(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		panic(err)
	}

	if (duplicateCount <= 0) {
		fmt.Printf("No processing needed: %d duplicates found\n", duplicateCount)
		return
	}

	fmt.Printf("Found and removed %d duplicate identifiers in %s.\nFile without dupes stored as deduped_%s\n", duplicateCount, inputPath, inputPath)
}
