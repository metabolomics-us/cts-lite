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
	"strconv"
	"time"

	_ "github.com/mattn/go-sqlite3"
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

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Pragmas tuned for write-once bulk insert - no crash recovery needed
	for _, pragma := range []string{
		"PRAGMA journal_mode = OFF",
		"PRAGMA synchronous = OFF",
		"PRAGMA locking_mode = EXCLUSIVE",
		"PRAGMA cache_size = -524288",
		tempStorePragma(),
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to apply pragma %q: %w", pragma, err)
		}
	}

	// Create table without indices first - building indices after all data is
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

// tempStorePragma keeps SQLite's temporary data (chiefly the sorter behind
// CREATE INDEX over ~13M rows) in memory, unless SQLITE_TMPDIR is set: then
// it spills to files in that directory. CI sets it, because the in-memory
// sort pushes a memory-capped build agent to its limit; SQLite itself reads
// SQLITE_TMPDIR to place the files.
func tempStorePragma() string {
	if os.Getenv("SQLITE_TMPDIR") != "" {
		return "PRAGMA temp_store = FILE"
	}
	return "PRAGMA temp_store = MEMORY"
}

// bulkInsert inserts all CSV rows using batched transactions for performance
// CSV column order: identifier, literature_count, patent_count, annotation_type_count,
//	molecular_formula, smiles, inchi, inchikey, exact_mass, compound_name
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

		if len(line) != 10 {
			tx.Rollback()
			return 0, fmt.Errorf("row %d has %d fields, expected 10", count+1, len(line))
		}

		// Skip lines without inchikeys
		if line[7] == "" {
			continue
		}

		// Skip rows with a missing formula or non-numeric fields
		if !hasValidNumericFields(line) {
			log.Printf("skipping row %d (identifier %s): missing or non-numeric fields", count+1, line[0])
			continue
		}

		if _, err := stmt.Exec(
			line[0],      // identifier
			line[7],      // inchikey
			line[7][:14], // first_block
			line[6],      // inchi
			line[5],      // smiles
			line[9],      // compound_name
			line[4],      // molecular_formula
			line[8],      // exact_mass
			line[1],      // literature_count
			line[2],      // patent_count
			line[3],      // annotation_type_count
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

// hasValidNumericFields checks that molecular_formula is present and that
// exact_mass, literature_count, patent_count, and annotation_type_count all
// parse as numbers
func hasValidNumericFields(line []string) bool {
	if line[4] == "" { // molecular_formula
		return false
	}
	for _, idx := range []int{8, 1, 2, 3} { // exact_mass, literature_count, patent_count, annotation_type_count
		if _, err := strconv.ParseFloat(line[idx], 64); err != nil {
			return false
		}
	}
	return true
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
