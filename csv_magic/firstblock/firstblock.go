package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
)

func main() {
	// Open the input CSV
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run firstblock.go <input.csv>")
		return
	}

	filePath := os.Args[1]
	f, err := os.Open(filePath)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		panic(err)
	}

	// Assume the first row is header
	header := records[0]

	// 1. Add a new column header for FirstBlock
	newColumnName := "FirstBlock"
	header = append(header, newColumnName)

	// 2. Find the index of the InChIKey column (case-insensitive)
	inchikeyIndex := -1
	for i, col := range records[0] {
		if col == "InChIKey" {
			inchikeyIndex = i
			break
		}
	}

	// Prepare new records including the added FirstBlock value per row
	newRecords := [][]string{header}
	for _, row := range records[1:] {
		firstBlock := ""
		if inchikeyIndex >= 0 && inchikeyIndex < len(row) {
			ik := strings.TrimSpace(row[inchikeyIndex])
			if ik != "" {
				// Get substring before first '-' then take up to 14 chars
				parts := strings.SplitN(ik, "-", 2)
				if len(parts) > 0 {
					raw := parts[0]
					if len(raw) >= 14 {
						firstBlock = raw[:14]
					} else {
						firstBlock = raw
					}
				}
			}
		}

		// Append the new value and add to output
		row = append(row, firstBlock)
		newRecords = append(newRecords, row)
	}

	// Write out to a new CSV
	outFile, err := os.Create("firstblocks_" + filePath)
	if err != nil {
		panic(err)
	}
	defer outFile.Close()

	w := csv.NewWriter(outFile)
	err = w.WriteAll(newRecords) // WriteAll handles flushing
	if err != nil {
		panic(err)
	}

	fmt.Println("FirstBlock column added successfully!")
	fmt.Println("Output written to firstblocks_" + filePath)
}
