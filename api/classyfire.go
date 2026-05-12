package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"ctslite/model"
)

const cfbBaseURL = "https://cfb.fiehnlab.ucdavis.edu/entities"
const cfbTimeout = 10 * time.Second
const cfbCacheTTL = 24 * time.Hour
const cfbMaxConcurrent = 50

var cfbHTTPClient = &http.Client{Timeout: cfbTimeout}
var cfbCache sync.Map

type cfbCacheEntry struct {
	info    *model.ClassyFireInfo
	expires time.Time
}

type cfbAPIResponse struct {
	Kingdom      *cfbNode `json:"kingdom"`
	Superclass   *cfbNode `json:"superclass"`
	Class        *cfbNode `json:"class"`
	Subclass     *cfbNode `json:"subclass"`
	DirectParent *cfbNode `json:"direct_parent"`
	Description  string   `json:"description"`
}

type cfbNode struct {
	Name string `json:"name"`
}

// classyFireFetcher is a variable so tests can replace it with a mock.
var classyFireFetcher = defaultClassyFireFetcher

func defaultClassyFireFetcher(inchikey string) (*model.ClassyFireInfo, error) {
	if v, ok := cfbCache.Load(inchikey); ok {
		entry := v.(cfbCacheEntry)
		if time.Now().Before(entry.expires) {
			return entry.info, nil
		}
		cfbCache.Delete(inchikey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfbTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/%s.json", cfbBaseURL, inchikey), nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := cfbHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching classyfire: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		cfbCache.Store(inchikey, cfbCacheEntry{nil, time.Now().Add(cfbCacheTTL)})
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		sentinel := &model.ClassyFireInfo{Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		cfbCache.Store(inchikey, cfbCacheEntry{sentinel, time.Now().Add(cfbCacheTTL)})
		return sentinel, nil
	}

	var r cfbAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding classyfire response: %w", err)
	}

	info := &model.ClassyFireInfo{Description: r.Description}
	if r.Kingdom != nil {
		info.Kingdom = r.Kingdom.Name
	}
	if r.Superclass != nil {
		info.Superclass = r.Superclass.Name
	}
	if r.Class != nil {
		info.Class = r.Class.Name
	}
	if r.Subclass != nil {
		info.Subclass = r.Subclass.Name
	}
	if r.DirectParent != nil {
		info.DirectParent = r.DirectParent.Name
	}

	cfbCache.Store(inchikey, cfbCacheEntry{info, time.Now().Add(cfbCacheTTL)})
	return info, nil
}

// enrichWithClassyFire fetches ClassyFire classifications for all unique
// InChIKeys in matched results concurrently and attaches them to compounds.
// Lookup errors are logged and treated as no classification so the rest of
// the response is unaffected.
func enrichWithClassyFire(results []*model.SingleResult) {
	seen := make(map[string]bool)
	var keys []string
	for _, r := range results {
		for _, c := range r.Matches {
			if !seen[c.InChIKey] {
				seen[c.InChIKey] = true
				keys = append(keys, c.InChIKey)
			}
		}
	}
	if len(keys) == 0 {
		return
	}

	var mu sync.Mutex
	cfMap := make(map[string]*model.ClassyFireInfo, len(keys))

	sem := make(chan struct{}, cfbMaxConcurrent)
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			info, err := classyFireFetcher(k)
			if err != nil {
				log.Printf("ClassyFire lookup failed for %s: %v", k, err)
				return
			}
			mu.Lock()
			cfMap[k] = info
			mu.Unlock()
		}(key)
	}
	wg.Wait()

	for _, r := range results {
		for _, c := range r.Matches {
			if info, ok := cfMap[c.InChIKey]; ok {
				c.ClassyFire = info
			}
		}
	}
}
