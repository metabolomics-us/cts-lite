package main

import (
	"ctslite/model"
	"database/sql"
	"encoding/csv"
	"os"
	"strings"
	"testing"
)

const testCSVContent = `Identifier,Literature_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,MonoisotopicMass,CompoundName
1,10,2,H2O,O,InChI=1S/H2O/h1H2,MYFAKEINCHIKEY-ISRIGHTHER-E,100,Water
2,18,7,CH4,C,InChI=1S/CH4/h1H4,MYFAKEINCHIKEY-ANOTHERONE-E,99,Methane
`

// writeTempCSV writes testCSVContent to a temp file and returns its path.
func writeTempCSV(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.csv")
	if err != nil {
		t.Fatalf("failed to create temp CSV: %v", err)
	}
	if _, err := f.WriteString(testCSVContent); err != nil {
		t.Fatalf("failed to write temp CSV: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestRun_Success(t *testing.T) {
	csvPath := writeTempCSV(t)
	dbPath := csvPath + ".db"
	t.Cleanup(func() { os.Remove(dbPath) })

	if err := run(csvPath, dbPath); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	// Verify the database was created and contains the right data
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open result DB: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM compounds").Scan(&count); err != nil {
		t.Fatalf("failed to count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}

	// Spot-check a known row
	var name string
	err = db.QueryRow("SELECT compound_name FROM compounds WHERE inchikey = ?",
		"MYFAKEINCHIKEY-ISRIGHTHER-E").Scan(&name)
	if err != nil {
		t.Fatalf("failed to query compound: %v", err)
	}
	if name != "Water" {
		t.Errorf("expected Water, got %s", name)
	}
}

func TestRun_SkipsExistingDB(t *testing.T) {
	csvPath := writeTempCSV(t)

	// Pre-create a file at the DB path so run() should skip
	existing, err := os.CreateTemp(t.TempDir(), "existing-*.db")
	if err != nil {
		t.Fatalf("failed to create existing DB file: %v", err)
	}
	existing.Close()
	dbPath := existing.Name()

	// run() should return nil without overwriting the existing file
	if err := run(csvPath, dbPath); err != nil {
		t.Fatalf("expected run to return nil when DB exists, got: %v", err)
	}

	// The file should still be empty (untouched)
	info, _ := os.Stat(dbPath)
	if info.Size() != 0 {
		t.Errorf("expected existing DB to be untouched (size 0), got size %d", info.Size())
	}
}

func TestRun_BadCSVPath(t *testing.T) {
	err := run("/nonexistent/path/to.csv", t.TempDir()+"/out.db")
	if err == nil {
		t.Error("expected error for missing CSV, got nil")
	}
}

func TestBulkInsert(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to open in-memory DB: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(model.CreateTableSQL); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Strip the header line — bulkInsert expects a reader already past the header
	lines := strings.SplitN(testCSVContent, "\n", 2)
	reader := csv.NewReader(strings.NewReader(lines[1]))

	count, err := bulkInsert(db, reader, batchSize)
	if err != nil {
		t.Fatalf("bulkInsert failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows inserted, got %d", count)
	}
}

func TestBulkInsert_MalformedCSV(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open in-memory DB: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(model.CreateTableSQL); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// A row with only 3 fields will trigger a scan error inside bulkInsert
	// when it tries to access line[9] (compound_name)
	malformed := "a,b,c\n"
	reader := csv.NewReader(strings.NewReader(malformed))

	_, err = bulkInsert(db, reader, batchSize)
	if err == nil {
		t.Error("expected error for malformed CSV row, got nil")
	}
}

// TestBulkInsert_BatchCommit exercises the mid-loop batch commit path by using
// a batch size of 1, which forces a commit after every row.
func TestBulkInsert_BatchCommit(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open in-memory DB: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(model.CreateTableSQL); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	lines := strings.SplitN(testCSVContent, "\n", 2)
	reader := csv.NewReader(strings.NewReader(lines[1]))

	count, err := bulkInsert(db, reader, 1)
	if err != nil {
		t.Fatalf("bulkInsert failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows inserted, got %d", count)
	}
}
