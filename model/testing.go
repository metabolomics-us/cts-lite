package model

// These methods are only used by the tests, to load a 'mock' database

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// LoadCSVToMemory builds a shared-cache in-memory SQLite database from a CSV
// file. Used by tests so they don't require a pre-built .db file.
func LoadCSVToMemory(csvPath string) (*PubChemIndex, error) {
	// Shared-cache in-memory DB so all connections in the pool see the same data
	return loadCSVToSQLite(csvPath, "file::memory:?cache=shared")
}

// LoadCSVToPrivateMemory builds a private (non-shared) in-memory SQLite
// database from a CSV file. Unlike LoadCSVToMemory, schema changes on the
// returned index do not affect other indexes in the same process.
func LoadCSVToPrivateMemory(csvPath string) (*PubChemIndex, error) {
	return loadCSVToSQLite(csvPath, ":memory:")
}

// DB returns the underlying *sql.DB. Exposed so tests in other packages can
// perform schema surgery (e.g. dropping a column to simulate an error path)
// without modifying the model API.
func (idx *PubChemIndex) DB() *sql.DB {
	return idx.db
}

func loadCSVToSQLite(csvPath, dsn string) (*PubChemIndex, error) {
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

	db, err := sql.Open("sqlite", dsn)
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
// CSV column order: identifier, literature_count, patent_count,
//	molecular_formula, smiles, inchi, inchikey, exact_mass, compound_name
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
			line[6], // inchikey
			line[6][:14], // first_block
			line[5], // inchi
			line[4], // smiles
			line[8], // compound_name
			line[3], // molecular_formula
			line[7], // exact_mass
			line[1], // literature_count
			line[2], // patent_count
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
