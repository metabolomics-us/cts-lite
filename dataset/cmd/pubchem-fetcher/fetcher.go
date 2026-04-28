package main 

// Usage:
// go run fetcher.go <cid-list-file>

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Property struct {
	CID              int    `json:"CID"`
	InChIKey         string `json:"InChIKey"`
	InChI            string `json:"InChI"`
	SMILES           string `json:"SMILES"`
	ExactMass        string `json:"ExactMass"`
	LiteratureCount  int    `json:"LiteratureCount"`
	PatentCount      int    `json:"PatentCount"`
	Title            string `json:"Title"`
	MolecularFormula string `json:"MolecularFormula"`
}

type PugResponse struct {
	PropertyTable struct {
		Properties []Property `json:"Properties"`
	} `json:"PropertyTable"`
}

func fetchPropertiesBatch(cids []int) (*PugResponse, error) {
	cidStrs := make([]string, len(cids))
	for i, c := range cids {
		cidStrs[i] = fmt.Sprintf("%d", c)
	}
	const endpoint = "https://pubchem.ncbi.nlm.nih.gov/rest/pug/compound/cid/property/InChIKey,InChI,SMILES,ExactMass,LiteratureCount,PatentCount,Title,MolecularFormula/JSON"
	body := url.Values{"cid": {strings.Join(cidStrs, ",")}}.Encode()

	// Simple retry
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			log.Printf("Retrying request, attempt %d", attempt+1)
		}
		resp, err := http.Post(endpoint, "application/x-www-form-urlencoded", strings.NewReader(body))
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
			if resp.StatusCode == 403 {
				log.Printf("Request denied, waiting 10s for rate limit reset")
				time.Sleep(10 * time.Second)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			continue
		}
		var pr PugResponse
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return &pr, nil
	}
	return nil, lastErr
}

func readCIDsFromFile(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cids []int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cid, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("invalid CID %q: %w", line, err)
		}
		cids = append(cids, cid)
	}
	return cids, scanner.Err()
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: fetcher <cid-list-file>")
	}

	allCIDs, err := readCIDsFromFile(os.Args[1])
	if err != nil {
		log.Fatalf("read CID file: %v", err)
	}
	log.Printf("Loaded %d CIDs from %s", len(allCIDs), os.Args[1])

	fileName := fmt.Sprintf("pubchem_rows%v.csv", time.Now().Unix())
	if len(os.Args) > 2 && os.Args[2] != "" {
		fileName = os.Args[2]
	}

	outFile, err := os.Create(fileName)
	if err != nil {
		log.Fatalf("create csv: %v", err)
	}
	defer outFile.Close()

	w := csv.NewWriter(outFile)
	defer w.Flush()

	// Header:
	w.Write([]string{"Compound_CID", "Linked_PubChem_Literature_Count", "Linked_PubChem_Patent_Count", "Molecular_Formula", "SMILES", "InChI", "InChIKey", "Exact_Mass", "Name"})

	batchSize := 300

	// Iterate in batches
	for i := 0; i < len(allCIDs); i += batchSize {
		end := i + batchSize
		if end > len(allCIDs) {
			end = len(allCIDs)
		}
		batch := allCIDs[i:end]
		log.Printf("Making request, %d/%d CIDs", end, len(allCIDs))

		pr, err := fetchPropertiesBatch(batch)
		if err != nil {
			log.Printf("fetch batch [%d:%d] failed: %v", i, end, err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, p := range pr.PropertyTable.Properties {
			row := []string{
				fmt.Sprintf("%d", p.CID),
				fmt.Sprintf("%d", p.LiteratureCount),
				fmt.Sprintf("%d", p.PatentCount),
				p.MolecularFormula,
				p.SMILES,
				p.InChI,
				p.InChIKey,
				p.ExactMass,
				p.Title,
			}
			if err := w.Write(row); err != nil {
				log.Printf("csv write error: %v", err)
			}
		}

		w.Flush()
		if err := w.Error(); err != nil {
			log.Fatalf("csv writer error: %v", err)
		}

		// Pause to avoid rate limiting
		time.Sleep(250 * time.Millisecond)
	}
}
