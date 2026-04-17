package model

import (
	"database/sql"
	"fmt"
	"runtime"

	_ "modernc.org/sqlite" // SQLite driver
)

type Compound struct {
	Identifier       string  `json:"identifier"`
	InChIKey         string  `json:"inchikey"`
	InChI            string  `json:"inchi"`
	Smiles           string  `json:"smiles"`
	CompoundName     string  `json:"compound_name"`
	MolecularFormula string  `json:"molecular_formula"`
	MonoisotopicMass float64 `json:"monoisotopic_mass"`
	LiteratureCount  float32 `json:"literature_count"`
	PatentCount      float32 `json:"patent_count"`
}

type SingleResult struct {
	Query      string      `json:"query"`
	QueryType  string      `json:"query_type"`
	MatchFound bool        `json:"found_match"`
	MatchLevel string      `json:"match_level"`
	Matches    []*Compound `json:"matches"`
	ErrMsg     string      `json:"error_message"`
}

// PubChemIndex wraps an SQLite database and prepared statements for each lookup type
type PubChemIndex struct {
	db           *sql.DB
	byPubChemID  *sql.Stmt
	byInChIKey   *sql.Stmt
	byFirstBlock *sql.Stmt
	byInChI      *sql.Stmt
	bySmiles     *sql.Stmt
	byFormula    *sql.Stmt
}

const selectCols = `SELECT identifier, inchikey, inchi, smiles, compound_name,
	molecular_formula, monoisotopic_mass, literature_count, patent_count FROM compounds`
const orderByScore = ` ORDER BY (0.7 * literature_count + 0.3 * patent_count) DESC`

// OpenSQLiteIndex opens a pre-built SQLite database for production use
func OpenSQLiteIndex(dbPath string) (*PubChemIndex, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Performance tuning: mmap lets the OS page cache serve reads directly from
	//   the mapped file, reducing Go heap and GC pressure
	pragmas := []string{
		"PRAGMA mmap_size = 2147483648",  // 2 GB memory-mapped I/O (adjusted for ECS instance resources)
		"PRAGMA cache_size = -131072",    // 128 MB SQLite page cache
		"PRAGMA temp_store = MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to apply pragma %q: %w", p, err)
		}
	}

	// WAL mode allows multiple concurrent readers; it was set at build time and
	//   is persisted in the DB file, so we don't need to set it again here
	db.SetMaxOpenConns(runtime.NumCPU() * 2)
	db.SetMaxIdleConns(runtime.NumCPU())

	return newIndex(db)
}

// newIndex prepares all statements on an already-configured *sql.DB
func newIndex(db *sql.DB) (*PubChemIndex, error) {
	idx := &PubChemIndex{db: db}

	stmts := []struct {
		dest  **sql.Stmt
		query string
	}{
		{&idx.byPubChemID,  selectCols + ` WHERE identifier = ?` + orderByScore},
		{&idx.byInChIKey,   selectCols + ` WHERE inchikey = ?` + orderByScore},
		{&idx.byFirstBlock, selectCols + ` WHERE first_block = ?` + orderByScore},
		{&idx.byInChI,      selectCols + ` WHERE inchi = ?` + orderByScore},
		{&idx.bySmiles,     selectCols + ` WHERE smiles = ?` + orderByScore},
		{&idx.byFormula,    selectCols + ` WHERE molecular_formula = ?` + orderByScore},
	}

	for _, s := range stmts {
		stmt, err := db.Prepare(s.query)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to prepare statement: %w", err)
		}
		*s.dest = stmt
	}

	return idx, nil
}

// CreateTableSQL and CreateIndexSQL are exported so cmd/build-db can reuse them
const CreateTableSQL = `CREATE TABLE IF NOT EXISTS compounds (
	identifier        TEXT NOT NULL,
	inchikey          TEXT NOT NULL,
	first_block       TEXT NOT NULL,
	inchi             TEXT NOT NULL,
	smiles            TEXT NOT NULL,
	compound_name     TEXT NOT NULL,
	molecular_formula TEXT NOT NULL,
	monoisotopic_mass REAL NOT NULL,
	literature_count  REAL NOT NULL,
	patent_count      REAL NOT NULL
)`

const CreateIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_pubchem_id  ON compounds(identifier);
CREATE INDEX IF NOT EXISTS idx_inchikey    ON compounds(inchikey);
CREATE INDEX IF NOT EXISTS idx_first_block ON compounds(first_block);
CREATE INDEX IF NOT EXISTS idx_inchi       ON compounds(inchi);
CREATE INDEX IF NOT EXISTS idx_smiles      ON compounds(smiles);
CREATE INDEX IF NOT EXISTS idx_formula     ON compounds(molecular_formula)`

const InsertSQL = `INSERT INTO compounds
	(identifier, inchikey, first_block, inchi, smiles, compound_name, molecular_formula, monoisotopic_mass, literature_count, patent_count)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// query executes a prepared statement and scans all result rows into Compound pointers
func (idx *PubChemIndex) query(stmt *sql.Stmt, arg string) ([]*Compound, error) {
	rows, err := stmt.Query(arg)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var compounds []*Compound
	for rows.Next() {
		c := &Compound{}
		if err := rows.Scan(
			&c.Identifier, &c.InChIKey, &c.InChI, &c.Smiles, &c.CompoundName,
			&c.MolecularFormula, &c.MonoisotopicMass, &c.LiteratureCount, &c.PatentCount,
		); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		compounds = append(compounds, c)
	}
	return compounds, rows.Err()
}

func (idx *PubChemIndex) QueryByPubChemID(id string) ([]*Compound, error) {
	return idx.query(idx.byPubChemID, id)
}

func (idx *PubChemIndex) QueryByInChIKey(key string) ([]*Compound, error) {
	return idx.query(idx.byInChIKey, key)
}

func (idx *PubChemIndex) QueryByFirstBlock(block string) ([]*Compound, error) {
	return idx.query(idx.byFirstBlock, block)
}

func (idx *PubChemIndex) QueryByInChI(inchi string) ([]*Compound, error) {
	return idx.query(idx.byInChI, inchi)
}

func (idx *PubChemIndex) QueryBySmiles(smiles string) ([]*Compound, error) {
	return idx.query(idx.bySmiles, smiles)
}

func (idx *PubChemIndex) QueryByFormula(formula string) ([]*Compound, error) {
	return idx.query(idx.byFormula, formula)
}

// Close releases the database connection and all prepared statements.
func (idx *PubChemIndex) Close() error {
	return idx.db.Close()
}
