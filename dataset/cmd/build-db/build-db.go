// Converts a CTS-Lite CSV dataset into a SQLite database

// Usage:
//   go run build-db.go <input.csv> <output.db>

package main

import (
	"ctslite/model"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

const batchSize = 100_000

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("Usage: build-db <input.csv> <output.db>")
	}
	csvPath := os.Args[1]
	dbPath := os.Args[2]

	if err := run(csvPath, dbPath); err != nil {
		log.Fatalf("build-db failed: %v", err)
	}
}

func run(csvPath, dbPath string) error {
	start := time.Now()

	f, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("failed to open CSV: %w", err)
	}
	defer f.Close()

	// If the database already exists, warn the user and exit
	if _, err := os.Stat(dbPath); err == nil {
		fmt.Printf("Database already exists at %s. Skipping build...\n", dbPath)
		return nil
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Pragmas tuned for bulk insert throughput
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -524288", // 512 MB during build
		"PRAGMA temp_store = MEMORY",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to apply pragma %q: %w", pragma, err)
		}
	}

	// Create table without indices first — building indices after all data is
	//   inserted is much faster than maintaining them row-by-row
	if _, err := db.Exec(model.CreateTableSQL); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	reader := csv.NewReader(f)
	_, _ = reader.Read() // skip header

	count, err := bulkInsert(db, reader, batchSize)
	if err != nil {
		return err
	}
	fmt.Printf("Inserted %d compounds in %.1f minutes\n", count, time.Since(start).Minutes())

	fmt.Println("Building indices...")
	indexStart := time.Now()
	if _, err := db.Exec(model.CreateIndexSQL); err != nil {
		return fmt.Errorf("failed to create indices: %w", err)
	}
	fmt.Printf("Indices built in %.1f minutes\n", time.Since(indexStart).Minutes())

	fmt.Printf("Done. Database written to %s (total %.1f minutes)\n", dbPath, time.Since(start).Minutes())
	return nil
}

// bulkInsert inserts all CSV rows using batched transactions for performance
// CSV column order: identifier, literature_count, patent_count,
//   molecular_formula, smiles, inchi, inchikey, monoisotopic_mass, compound_name
func bulkInsert(db *sql.DB, reader *csv.Reader, batchSize int) (int, error) {
	tx, stmt, err := beginBatch(db)
	if err != nil {
		return 0, err
	}

	count := 0
	for {
		line, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("failed to read CSV row: %w", err)
		}

		if len(line) != 9 {
			tx.Rollback()
			return 0, fmt.Errorf("row %d has %d fields, expected 9", count+1, len(line))
		}

		if _, err := stmt.Exec(
			line[0], // identifier
			line[6], // inchikey
			line[6][:14], // first_block
			line[5], // inchi
			line[4], // smiles
			line[8], // compound_name
			line[3], // molecular_formula
			line[7], // monoisotopic_mass
			line[1], // literature_count
			line[2], // patent_count
		); err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("failed to insert row %d: %w", count+1, err)
		}

		count++

		if count%batchSize == 0 {
			stmt.Close()
			if err := tx.Commit(); err != nil {
				return 0, fmt.Errorf("failed to commit batch at row %d: %w", count, err)
			}
			if count%(batchSize*10) == 0 {
				fmt.Printf("  %d rows inserted...\n", count)
			}
			tx, stmt, err = beginBatch(db)
			if err != nil {
				return 0, err
			}
		}
	}

	stmt.Close()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit final batch: %w", err)
	}

	return count, nil
}

func beginBatch(db *sql.DB) (*sql.Tx, *sql.Stmt, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	stmt, err := tx.Prepare(model.InsertSQL)
	if err != nil {
		tx.Rollback()
		return nil, nil, fmt.Errorf("failed to prepare insert: %w", err)
	}
	return tx, stmt, nil
}
