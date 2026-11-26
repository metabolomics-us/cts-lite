// Adds an HMDB column to a CTS-Lite CSV dataset

// Usage:
//   go run hmdb-join.go <input.csv> <output.csv>

package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const sdqURL = "https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi"

// The source name HMDB deposits under
const hmdbSource = "Human Metabolome Database (HMDB)"

// Every HMDB substance with its CID and HMDB ID
const sdqQuery = `{"download":"sid,cid,sidextid","collection":"substance","where":{"ands":[{"sidsrcname":"` +
	hmdbSource + `"}]},"order":["sid,asc"],"start":1,"limit":10000000}`

// Columns a dataset has before the HMDB column is added
const inputColumns = 11

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("Usage: hmdb-join <input.csv> <output.csv>")
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		log.Fatalf("hmdb-join failed: %v", err)
	}
}

func run(inPath, outPath string) error {
	start := time.Now()

	in, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("failed to open CSV: %w", err)
	}
	defer in.Close()

	// Checked before the download so a wrong file fails in seconds instead of minutes
	reader := csv.NewReader(in)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("failed to read CSV header: %w", err)
	}
	if len(header) != inputColumns {
		return fmt.Errorf("%s has %d columns, expected %d (does it already have an HMDB column?)", inPath, len(header), inputColumns)
	}

	fmt.Println("Downloading HMDB substances from PubChem...")
	ids, err := fetchHMDB(sdqURL)
	if err != nil {
		return err
	}
	fmt.Printf("Found HMDB IDs for %d CIDs in %.1f seconds\n", len(ids), time.Since(start).Seconds())

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("failed to create output CSV: %w", err)
	}
	defer out.Close()

	rows, withIDs, err := joinHMDB(reader, header, csv.NewWriter(out), ids)
	if err != nil {
		return err
	}
	fmt.Printf("%d of %d compounds have an HMDB ID. Written to %s (total %.1f minutes)\n", withIDs, rows, outPath, time.Since(start).Minutes())
	return nil
}

// fetchHMDB downloads HMDB's substances from SDQ and returns the HMDB IDs per CID
func fetchHMDB(endpoint string) (map[string][]string, error) {
	params := url.Values{"infmt": {"json"}, "outfmt": {"csv"}, "query": {sdqQuery}}
	resp, err := http.Get(endpoint + "?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("failed to download HMDB substances: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download HMDB substances: %s", resp.Status)
	}

	ids, err := parseSDQ(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HMDB substances: %w", err)
	}
	// An empty column would build fine and quietly lose every HMDB lookup
	if len(ids) == 0 {
		return nil, fmt.Errorf("no %q substances returned, has the source name changed?", hmdbSource)
	}
	return ids, nil
}

// parseSDQ returns the sorted HMDB IDs per CID from SDQ's sid,cid,sidextid CSV
func parseSDQ(r io.Reader) (map[string][]string, error) {
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	cidCol, idCol := slices.Index(header, "cid"), slices.Index(header, "sidextid")
	if cidCol < 0 || idCol < 0 {
		return nil, fmt.Errorf("unexpected response, starting %q", strings.Join(header, ","))
	}

	ids := map[string][]string{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		cid, id := row[cidCol], row[idCol]
		if cid == "" || slices.Contains(ids[cid], id) {
			continue
		}
		ids[cid] = append(ids[cid], id)
	}
	for _, list := range ids {
		slices.Sort(list)
	}
	return ids, nil
}

// joinHMDB copies the dataset with each CID's HMDB IDs appended, "|" separated like Synonyms
func joinHMDB(reader *csv.Reader, header []string, writer *csv.Writer, ids map[string][]string) (rows, withIDs int, err error) {
	if err := writer.Write(append(header, "HMDB")); err != nil {
		return 0, 0, fmt.Errorf("failed to write CSV header: %w", err)
	}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rows, withIDs, fmt.Errorf("failed to read CSV row %d: %w", rows+1, err)
		}
		hmdb := strings.Join(ids[row[0]], "|")
		if hmdb != "" {
			withIDs++
		}
		if err := writer.Write(append(row, hmdb)); err != nil {
			return rows, withIDs, fmt.Errorf("failed to write CSV row %d: %w", rows+1, err)
		}
		rows++
	}
	writer.Flush()
	return rows, withIDs, writer.Error()
}
