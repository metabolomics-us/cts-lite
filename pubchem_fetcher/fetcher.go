package fetcher

import (
	"ctslite/model"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

func firstBlockFromInchiKey(ik string) string {
	if ik == "" {
		return ""
	}
	parts := strings.SplitN(ik, "-", 2)
	return parts[0]
}

func fetchPropertiesBatch(cids []int) (*PugResponse, error) {
	cidStrs := make([]string, len(cids))
	for i, c := range cids {
		cidStrs[i] = fmt.Sprintf("%d", c)
	}
	cidList := strings.Join(cidStrs, ",")
	url := fmt.Sprintf("https://pubchem.ncbi.nlm.nih.gov/rest/pug/compound/cid/%s/property/InChIKey,InChI,SMILES,ExactMass,LiteratureCount,PatentCount,Title,MolecularFormula/JSON", cidList)

	log.Printf("Making request")

	// Simple retry
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			log.Printf("Retrying request, attempt %d", attempt+1)
		}
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
			time.Sleep(500 * time.Millisecond)
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

func main() {
	index, err := model.LoadPubChemLite("../data/PubChemLite_CCSbase_20251128_trimmed.csv")
	if err != nil {
		log.Fatalf("Failed to load index: %v", err)
	}

	// Use timestamped filename to avoid overwriting
	fileName := fmt.Sprintf("pubchem_rows%v.csv", time.Now().Unix())
	outFile, err := os.Create(fileName)

	if err != nil {
		log.Fatalf("create csv: %v", err)
	}
	defer outFile.Close()

	w := csv.NewWriter(outFile)
	defer w.Flush()

	// header:
	w.Write([]string{"Identifier", "FirstBlock", "PubMed_Count", "Patent_Count", "MolecularFormula", "SMILES", "InChI", "InChIKey", "MonoisotopicMass", "CompoundName"})

	batchSize := 400
	maxCID := 1000000
	startCID := 1 // change as needed (currently 1 to 1 million)

	// iterate in batches
	for i := startCID; i <= maxCID; i += batchSize {
		end := i + batchSize - 1
		if end > maxCID {
			end = maxCID
		}
		cids := make([]int, 0, end-i+1)
		for c := i; c <= end; c++ {
			cids = append(cids, c)
		}

		pr, err := fetchPropertiesBatch(cids)
		if err != nil {
			log.Printf("fetch batch %d-%d failed: %v", i, end, err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, p := range pr.PropertyTable.Properties {
			// filter out compounds without any patents or literature
			if p.LiteratureCount == 0 && p.PatentCount == 0 {
				continue
			} else if index.ByInChIKey[p.InChIKey] != nil {
				// already in our index
				continue
			}

			firstBlock := firstBlockFromInchiKey(p.InChIKey)

			row := []string{
				fmt.Sprintf("%d", p.CID),
				firstBlock,
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

		// pause to avoid rate limiting
		time.Sleep(350 * time.Millisecond)
	}
}
