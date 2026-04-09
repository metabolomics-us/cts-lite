package model

import (
	"time"
	"log"
	"os"
	"fmt"
	"database/sql"
	"encoding/csv"
	"io"
)

// LoadCSVToMemory builds an in-memory SQLite database from a CSV file
// Used by tests so they don't require a pre-built .db file
func LoadCSVToMemory(csvPath string) (*PubChemIndex, error) {
	startTime := time.Now()

	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("Failed to close CSV file: %v", cerr)
		}
	}()

	// Shared-cache in-memory DB so all connections in the pool see the same data
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory database: %w", err)
	}
	// Single connection ensures all operations share the same in-memory DB
	db.SetMaxOpenConns(1)

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header

	if err := populateDB(db, reader); err != nil {
		db.Close()
		return nil, err
	}

	log.Printf("Loaded CSV into memory in %.2f seconds", time.Since(startTime).Seconds())
	return newIndex(db)
}

// populateDB creates the schema and bulk-inserts rows from a CSV reader (for tests)
// CSV column order: identifier, first_block, pubmed_count, patent_count,
//   molecular_formula, smiles, inchi, inchikey, monoisotopic_mass, compound_name
func populateDB(db *sql.DB, reader *csv.Reader) error {
	if _, err := db.Exec(CreateTableSQL); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	stmt, err := tx.Prepare(InsertSQL)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to read CSV row: %w", err)
		}

		if _, err := stmt.Exec(
			line[0], // identifier
			line[7], // inchikey
			line[1], // first_block
			line[6], // inchi
			line[5], // smiles
			line[9], // compound_name
			line[4], // molecular_formula
			line[8], // monoisotopic_mass
			line[2], // pubmed_count
			line[3], // patent_count
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert row: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	if _, err := db.Exec(CreateIndexSQL); err != nil {
		return fmt.Errorf("failed to create indices: %w", err)
	}

	return nil
}

