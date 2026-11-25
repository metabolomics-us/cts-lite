package api

import (
	"ctslite/model"
	"ctslite/telemetry"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
)

// attachExternalIDs adds CAS and HMDB IDs to the results' matches. They only add
// detail, so a failed lookup is logged and the matches go out without them
func attachExternalIDs(index *model.PubChemIndex, results []*model.SingleResult) {
	var compounds []*model.Compound
	for _, r := range results {
		compounds = append(compounds, r.Matches...)
	}
	if err := index.AttachExternalIDs(compounds); err != nil {
		log.Printf("Failed to attach CAS/HMDB IDs: %v", err)
	}
}

// Query types with no ambiguity, so one on a line means the line is a list instead of a long name
var strongQueryTypes = map[string]bool{
	"inchikey": true, "inchi": true, "bad_inchikey": true, "bad_inchi": true, "cas": true, "bad_cas": true, "hmdb": true, "bad_hmdb": true,
}

// Query types that might be found inside a name (i.e. Tween 80, 80 is a CID)
var identifierQueryTypes = map[string]bool{
	"pubchem_id": true, "bad_pubchem_id": true, "formula": true, "smiles_or_formula": true,
}

// smilesStart are the characters a SMILES can start with
const smilesStart = "BCNOPSFIbcnops[*"

// smilesChars are every character a SMILES can contain
const smilesChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789=#/\\:.@[]()+-%*"

// isPlausibleSmiles rules out words that only read as SMILES because of their
// punctuation, like the "16:0_18:1" in "PC 16:0_18:1"
func isPlausibleSmiles(q string) bool {
	if q == "" || !strings.ContainsRune(smilesStart, rune(q[0])) {
		return false
	}
	for i := 0; i < len(q); i++ {
		if !strings.ContainsRune(smilesChars, rune(q[i])) {
			return false
		}
	}
	return true
}

// isIdentifierToken reports whether a word on a line can be an identifier of its own
func isIdentifierToken(t string) bool {
	queryType := parseQueryType(t)
	return strongQueryTypes[queryType] || identifierQueryTypes[queryType] ||
		(queryType == "smiles" && isPlausibleSmiles(t))
}

// Error msg for a whole line that missed as a name but held identifiers
const multiEntryNote = "No compound found. If this line holds several entries, put each on its own line"

// maxNameWords is the most words a line can have and still be read as one name.
// 99.92% of PubChem names have 10 words or fewer
const maxNameWords = 10

// tokenizeLine splits a line on whitespace, keeping each quoted span as one token.
// A quote opens only at the start of a word and closes only at the end of one, so a quote
// inside a word stays as written. An unmatched quote is dropped and its words read as unquoted.
// quoted reports whether the line had a non-empty quoted span. Mirrored by tokenizeLine in script.js
func tokenizeLine(line string) (tokens []string, quoted bool) {
	words := strings.Fields(line)
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, `"`) {
			// The closer is the first word ending in a quote, this word only if the quote is not its opener
			end := -1
			for j := i; j < len(words); j++ {
				if strings.HasSuffix(words[j], `"`) && (j > i || len(w) > 1) {
					end = j
					break
				}
			}
			if end >= 0 {
				span := slices.Clone(words[i : end+1])
				span[0] = span[0][1:]
				span[len(span)-1] = strings.TrimSuffix(span[len(span)-1], `"`)
				// Bare quotes inside the span are dropped like anywhere else
				span = slices.DeleteFunc(span, func(t string) bool { return strings.Trim(t, `"`) == "" })
				if len(span) > 0 {
					tokens = append(tokens, strings.Join(span, " "))
					quoted = true
				}
				i = end
				continue
			}
			w = w[1:] // unmatched opener
		}
		// A closer with nothing open is dropped
		if w = strings.TrimSuffix(w, `"`); strings.Trim(w, `"`) != "" {
			tokens = append(tokens, w)
		}
	}
	return tokens, quoted
}

// splitQueries makes one query per line, except a line that is a list of identifiers separated by spaces.
// Each quoted span is one query and every unquoted word beside it is its own, with no name guessing.
// A forced query type skips the guessing: a name is one per line, anything else splits on all whitespace.
// multiEntry holds the indexes of whole-line queries that carry identifiers, for multiEntryNote on a miss
func splitQueries(index *model.PubChemIndex, raw string, limit int, forced string) (queries []string, multiEntry map[int]bool) {
	multiEntry = map[int]bool{}
	for line := range strings.SplitSeq(raw, "\n") {
		if len(queries) > limit {
			break // early check against >100k queries
		}
		// Same logic found in script.js for counting queries
		tokens, quoted := tokenizeLine(line)
		if quoted || len(tokens) <= 1 {
			queries = append(queries, tokens...)
			continue
		}
		whole := strings.Join(tokens, " ")
		if forced == "name" {
			queries = append(queries, whole)
			continue
		}
		if forced != "" || len(tokens) > maxNameWords {
			queries = append(queries, tokens...)
			continue
		}
		// A strong identifier never sits inside a name, so the line splits without a name lookup
		anyStrong, anyIdentifier, allIdentifiers := false, false, true
		for _, t := range tokens {
			anyStrong = anyStrong || strongQueryTypes[parseQueryType(t)]
			isID := isIdentifierToken(t)
			anyIdentifier = anyIdentifier || isID
			allIdentifiers = allIdentifiers && isID
		}
		if anyStrong || (allIdentifiers && !isKnownName(index, whole)) {
			queries = append(queries, tokens...)
			continue
		}
		if anyIdentifier {
			multiEntry[len(queries)] = true
		}
		queries = append(queries, whole)
	}
	return queries, multiEntry
}

// isKnownName reports whether a whole line is a name in the database
func isKnownName(index *model.PubChemIndex, q string) bool {
	compounds, err := index.QueryByName(q, true)
	return err == nil && len(compounds) > 0
}

// maxQueries for one request (then with ClassyFire enabled)
const (
	maxQueries           = 100_000
	maxClassyFireQueries = 1_000
)

// Cheap check to see if a request is over the query limit by line count
func countQueryLines(raw string) int {
	n := 0
	for line := range strings.SplitSeq(raw, "\n") {
		if strings.ContainsFunc(line, func(r rune) bool { return r != '"' && !unicode.IsSpace(r) }) {
			n++
		}
	}
	return n
}

// queryInput is the "queries" field from a POST request
type queryInput struct {
	raw    string
	list   []string
	isList bool
}

func (q *queryInput) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &q.raw); err == nil {
		return nil
	}
	q.isList = true
	return json.Unmarshal(b, &q.list)
}

// cleanQuery trims a query and removes one pair of surrounding double quotes
func cleanQuery(q string) string {
	q = strings.TrimSpace(q)

	// Remove surrounding double quotes if both present
	if strings.HasPrefix(q, "\"") && strings.HasSuffix(q, "\"") && len(q) > 1 {
		q = q[1 : len(q)-1]
	}

	// Handle single double quote character
	if q == "\"" {
		return ""
	}
	return q
}

// Match is the main entry point for the API
// Detects the type of query and delegates it to the corresponding matching function
func Match(index *model.PubChemIndex, w http.ResponseWriter, r *http.Request) {
	var rawQuery string
	var input queryInput

	// Parse query according to GET or POST request (GET was the old method before adding POST)
	switch r.Method {

	case http.MethodGet:
		rawQuery = r.URL.Query().Get("q")

	case http.MethodPost:
		var request struct {
			Queries queryInput `json:"queries"`
		}

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		input = request.Queries
		rawQuery = strings.TrimSpace(input.raw)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if rawQuery == "" && len(input.list) == 0 {
		http.Error(w, "Query was empty", http.StatusBadRequest)
		return
	}

	// Check for request parameters
	var topHitOnly bool = r.URL.Query().Get("top_hit_only") != "false"
	var allowFirstBlockMatches bool = r.URL.Query().Get("first_block_matches") != "false"
	var classyfireEnabled bool = r.URL.Query().Get("classyfire") == "true"
	var stream bool = r.URL.Query().Get("stream") == "true"

	// Forces every query to one type, empty or "auto" detects each query's type
	forced := r.URL.Query().Get("query_type")
	if forced == "auto" {
		forced = ""
	}
	if _, ok := forcedQueryTypes[forced]; forced != "" && !ok {
		http.Error(w, "Invalid query_type, expected auto, pubchem_id, inchikey, inchi, smiles, formula, cas, hmdb or name", http.StatusBadRequest)
		return
	}

	// ClassyFire is rate limited, so it has a lower query limit
	limit, limitNote := maxQueries, "limit 100,000"
	if classyfireEnabled {
		limit, limitNote = maxClassyFireQueries, "limit 1,000 when ClassyFire is enabled"
	}
	tooMany := func() {
		http.Error(w, fmt.Sprintf("Query contains more than %d identifiers (%s)", limit, limitNote), http.StatusBadRequest)
	}

	var queries []string
	var multiEntry map[int]bool
	if input.isList {
		// Array input is never split
		if len(input.list) > limit {
			tooMany()
			return
		}
		for i, q := range input.list {
			if cleanQuery(q) == "" { // empty query throws 400
				http.Error(w, fmt.Sprintf("queries[%d] is empty", i), http.StatusBadRequest)
				return
			}
		}
		queries = input.list
	} else {
		// Every line makes at least one query, so too many lines fails before any name lookup
		if countQueryLines(rawQuery) > limit {
			tooMany()
			return
		}

		// Split query by newline, and by space on lines of identifiers
		queries, multiEntry = splitQueries(index, rawQuery, limit, forced)
		if len(queries) > limit {
			tooMany()
			return
		}
	}

	results := make([]*model.SingleResult, 0, len(queries))
	var matchCount int = 0
	timeStart := time.Now()

	// One span for the whole local SQLite pass, separate from the PubChem and
	// ClassyFire spans so a trace shows where a request spent its time
	_, localSpan := telemetry.StartSpan(r.Context(), "local_match")
	localMatches := 0

	for i, q := range queries {
		// Handle empty queries
		if q = cleanQuery(q); q == "" {
			continue
		}

		result := &model.SingleResult{
			Query:     q,
			QueryType: parseQueryType(q),
		}

		if forced != "" {
			matchForced(index, q, result, forced, allowFirstBlockMatches, topHitOnly)
			addForcedNote(result, forced)
			if result.MatchFound {
				localMatches++
			}
			results = append(results, result)
			continue
		}

		switch result.QueryType {
		case "pubchem_id":
			matchPubChemID(index, q, result, topHitOnly)

		case "inchi":
			matchInchi(index, q, result, topHitOnly)

		case "inchikey":
			matchInchiKey(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "smiles":
			matchSmiles(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "formula":
			matchFormula(index, q, result, topHitOnly)

		case "smiles_or_formula":
			matchSmilesOrFormula(index, q, result, allowFirstBlockMatches, topHitOnly)

		case "cas":
			matchCAS(index, q, result, topHitOnly)

		case "hmdb":
			matchHMDB(index, q, result, topHitOnly)

		case "bad_pubchem_id", "bad_inchi", "bad_inchikey", "bad_cas", "bad_hmdb":
			result.MatchFound = false
			result.ErrMsg = malformedQueryErrors[result.QueryType]

		// The name lookup itself runs later, if it doesn't find anything it keeps these properties
		case "name":
			result.MatchFound = false
			result.ErrMsg = "No compound found"

		case "unidentified":
			result.MatchFound = false
			result.ErrMsg = "Invalid query type, could not identify, see documentation"

		default:
			log.Printf("ERROR: An unexpected error occured when parsing the request. Query type unhandled. Query: '%s'", q)
			localSpan.End()
			http.Error(w, "An unexpected error occurred when parsing the request", http.StatusInternalServerError)
			return
		}

		// A name is only tried once the structure lookup found nothing
		if !result.MatchFound && result.ErrMsg != "Internal server error" && nameFallbackTypes[result.QueryType] && result.DerivedInChIKey == "" {
			matchName(index, q, result, topHitOnly)
		}

		// A line kept whole as a name that also held identifiers was likely several entries, warn user
		if !result.MatchFound && multiEntry[i] && result.ErrMsg != "Internal server error" {
			result.ErrMsg = multiEntryNote
		}

		if result.MatchFound {
			localMatches++
		}
		results = append(results, result)
	}

	// One batched lookup for every local match, PubChem hits get theirs as they settle
	attachExternalIDs(index, results)

	localSpan.SetAttributes(
		attribute.Int("local_match.queries", len(results)),
		attribute.Int("local_match.matches", localMatches),
	)
	localSpan.End()

	// Every query was a bare quote, so treat the request as blank
	if len(results) == 0 {
		http.Error(w, "Query was empty", http.StatusBadRequest)
		return
	}

	// Attempt a live PubChem lookup for anything that missed locally (except names and unidentified)
	// onSettled hears about each result as soon as its lookup is over, nil when not streaming
	fallback := func(onSettled func(index int)) {
		// A PubChem hit gets its CAS and HMDB IDs before anything sees the result
		settled := func(i int) {
			attachExternalIDs(index, results[i:i+1])
			// The fallback rewrites the error, so the forced note goes back on
			addForcedNote(results[i], forced)
			if onSettled != nil {
				onSettled(i)
			}
		}
		pcb := resolvePubChemFallbackStream(r.Context(), results, topHitOnly, settled)
		if pcb.attempted+pcb.unparseable+pcb.skipped > 0 {
			log.Printf("PubChem fallback: %d attempted, %d found, %d not found, %d failed, %d unparseable (not sent), %d skipped (due to limit reached)",
				pcb.attempted, pcb.found, pcb.notFound, pcb.failed, pcb.unparseable, pcb.skipped)
		}

		for _, result := range results {
			if result.MatchFound {
				matchCount++
			}
		}

		duration := time.Since(timeStart)
		log.Printf("%d matches found from %d queries in %s\n", matchCount, len(queries), time.Since(timeStart).Round(time.Millisecond))
		telemetry.RecordMatch(r, results, matchCount, duration, telemetry.MatchOptions{
			TopHitOnly:             topHitOnly,
			AllowFirstBlockMatches: allowFirstBlockMatches,
			ClassyFireEnabled:      classyfireEnabled,
		})
	}

	csvRequested := (r.Header.Get("Accept") == "text/csv") || (r.URL.Query().Get("format") == "csv")

	// Emit local matches immediately, then PubChem fallback results and
	// ClassyFire results as they come. Not possible for CSV
	if stream && !csvRequested {
		streamMatchResults(w, r, results, classyfireEnabled, fallback)
		return
	}

	// Non-streaming, write full response once the fallback and ClassyFire are done
	fallback(nil)
	if classyfireEnabled {
		enrichWithClassyFire(r.Context(), results)
	}

	if csvRequested {
		w.Header().Set("Content-Type", "text/csv")
		err := writeResultsAsCSV(w, results, classyfireEnabled)
		if err != nil {
			log.Printf("Failed to write CSV response: %v", err)
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(results)
		if err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			log.Printf("Failed to encode JSON response: %v", err)
		}
	}

}

// streamMatchResults writes the response as NDJSON: first the local matches, a
// pubchem line per fallback result as it settles, then ClassyFire if enabled
func streamMatchResults(w http.ResponseWriter, r *http.Request, results []*model.SingleResult, classyfireEnabled bool, fallback func(onSettled func(index int))) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fallback(nil)
		if classyfireEnabled {
			enrichWithClassyFire(r.Context(), results)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(results); err != nil &&
			!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			log.Printf("Failed to encode JSON response: %v", err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	enc := json.NewEncoder(w)
	var writeMu sync.Mutex // ClassyFire lines come from its own goroutine
	writeLine := func(v any) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := enc.Encode(v); err != nil {
			if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
				log.Printf("Failed to write stream message: %v", err)
			}
			return false
		}
		flusher.Flush()
		return true
	}

	// Rows still waiting on PubChem, each is replaced by a pubchem line once its answer is in
	misses := collectPubChemMisses(results)
	for _, m := range misses {
		m.result.Pending = true
	}

	// ClassyFire runs alongside the fallback
	var localKeys []string
	var keys chan string
	var cfDone chan struct{}
	seen := map[string]bool{}
	remaining := len(misses)
	inQueue := false

	// Upper bound on the # of compounds this request might classify (decreases with misses)
	bound := func() int { return len(seen) + remaining*cfbMaxMatchesPerQuery }
	lastBound := 0

	// enqueue sends the bound when it changes
	enqueue := func(newKeys []string) {
		var fresh []string
		for _, k := range newKeys {
			if !seen[k] {
				seen[k] = true
				fresh = append(fresh, k)
			}
		}
		// Frontend sees the queue depth before the first classification
		joined := false
		if len(fresh) > 0 && !inQueue {
			inQueue, joined = true, true
			cfbEnterQueue()
		}
		if b := bound(); b != lastBound || joined {
			lastBound = b
			writeLine(map[string]any{"type": "classifying", "unique": b, "queue": cfbQueueDepth()})
		}
		for _, k := range fresh {
			keys <- k
		}
	}
	defer func() {
		if inQueue {
			cfbLeaveQueue()
		}
	}()

	matches := map[string]any{"type": "matches", "results": results}
	if classyfireEnabled {
		// Already deduplicated, so this is the bound enqueue(localKeys) will send
		localKeys = classifiableKeys(results)
		lastBound = len(localKeys) + remaining*cfbMaxMatchesPerQuery
		matches["unique"] = lastBound
	}
	if !writeLine(matches) {
		return
	}

	if classyfireEnabled {
		// Room for every key this request can produce, so a send never stalls the fallback
		keys = make(chan string, len(localKeys)+remaining*cfbMaxMatchesPerQuery)
		cfDone = make(chan struct{})
		go func() {
			defer close(cfDone)
			// A panic here would take down the server without this defer
			defer func() {
				if p := recover(); p != nil {
					log.Printf("ClassyFire stream panicked: %v\n%s", p, debug.Stack())
				}
			}()
			streamClassyFire(r.Context(), keys, func(key string, info *model.ClassyFireInfo) {
				writeLine(map[string]any{"type": "classyfire", "inchikey": key, "info": info, "queue": cfbQueueDepth()})
			})
		}()
		enqueue(localKeys)
	}

	fallback(func(index int) {
		result := results[index]
		result.Pending = false
		var hitKeys []string
		if classyfireEnabled {
			// Attaches the top hit cap note before the line goes out
			hitKeys = classifiableKeys([]*model.SingleResult{result})
		}
		writeLine(map[string]any{"type": "pubchem", "index": index, "result": result})
		if classyfireEnabled {
			remaining--
			enqueue(hitKeys)
		}
	})

	if classyfireEnabled {
		close(keys)
		<-cfDone
	}
	writeLine(map[string]any{"type": "done"})
}

func Status(w http.ResponseWriter, _ *http.Request) {
	_, err := fmt.Fprintln(w, "The CTSLite server is up and running!")
	if err != nil {
		log.Printf("Status check failed to write response: %v", err)
		return
	}
}
