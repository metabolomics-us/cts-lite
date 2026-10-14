package telemetry

import (
	"context"
	"ctslite/model"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
)

const scopeName = "ctslite/telemetry"

// maxLoggedMisses caps how many missed entries are attached to each /match
// summary log, to bound log volume regardless of request size.
const maxLoggedMisses = 5

// MatchOptions carries the per-request /match flags into telemetry.
type MatchOptions struct {
	TopHitOnly             bool
	AllowFirstBlockMatches bool
	ClassyFireEnabled      bool
}

var (
	instrumentsOnce           sync.Once
	matchRequests             metric.Int64Counter
	matchQueries              metric.Int64Counter
	matchMatches              metric.Int64Counter
	matchQueryTypes           metric.Int64Counter
	matchHitPercent           metric.Float64Histogram
	matchDuration             metric.Float64Histogram
	matchQueriesPerReq        metric.Int64Histogram
	classyfireClassifications metric.Int64Counter
	pubchemFallbackOutcomes   metric.Int64Counter
	pubchemFallbackDuration   metric.Float64Histogram
	pubchemFallbackBatchSize  metric.Int64Histogram
	pubchemFallbackValues     metric.Int64Counter
	matchLogger               log.Logger
	classyfireGaugeOnce       sync.Once
	pubchemGaugeOnce          sync.Once
)

// initInstruments lazily creates the metric instruments and logger from the
// global providers. It runs on first use rather than at package init because
// the global OTel providers are only registered once main() calls Setup;
// creating instruments earlier would bind them to no-op providers.
func initInstruments() {
	instrumentsOnce.Do(func() {
		meter := otel.Meter(scopeName)
		matchRequests, _ = meter.Int64Counter("match_requests_total",
			metric.WithDescription("Number of /match requests"))
		matchQueries, _ = meter.Int64Counter("match_queries_total",
			metric.WithDescription("Number of individual queries processed by /match"))
		matchMatches, _ = meter.Int64Counter("match_matches_total",
			metric.WithDescription("Number of queries that produced a match"))
		matchQueryTypes, _ = meter.Int64Counter("match_query_type_total",
			metric.WithDescription("Distribution of detected query types, split by whether they matched"))
		matchHitPercent, _ = meter.Float64Histogram("match_hit_percent",
			metric.WithDescription("Percentage of queries matched per /match request"),
			metric.WithUnit("%"))
		matchDuration, _ = meter.Float64Histogram("match_duration_ms",
			metric.WithDescription("Duration of /match query matching"),
			metric.WithUnit("ms"))
		matchQueriesPerReq, _ = meter.Int64Histogram("match_queries_per_request",
			metric.WithDescription("Distribution of the number of queries per /match request"),
			metric.WithExplicitBucketBoundaries(1, 5, 50, 250, 1000, 5000, 25000, 100000))
		classyfireClassifications, _ = meter.Int64Counter("classyfire_classifications_total",
			metric.WithDescription("Terminal outcomes of individual ClassyFire classifications, split by status"))
		pubchemFallbackOutcomes, _ = meter.Int64Counter("pubchem_fallback_outcomes_total",
			metric.WithDescription("Terminal outcomes of live PubChem fallback lookups triggered by /match misses, split by status and query type"))
		// Its count doubles as the number of PubChem calls, so there is no separate request counter
		pubchemFallbackDuration, _ = meter.Float64Histogram("pubchem_fallback_request_duration_ms",
			metric.WithDescription("Duration of each live PubChem fallback HTTP round trip (retries included), split by endpoint and result"),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(50, 100, 250, 500, 1000, 2000, 5000, 10000, 30000))
		pubchemFallbackBatchSize, _ = meter.Int64Histogram("pubchem_fallback_batch_size",
			metric.WithDescription("Number of values carried by each live PubChem fallback HTTP round trip, split by endpoint"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 50, 100, 250, 500, 1000))
		pubchemFallbackValues, _ = meter.Int64Counter("pubchem_fallback_values_total",
			metric.WithDescription("Values sent to PubChem, split by endpoint and by what PubChem answered for each"))
		matchLogger = logglobal.GetLoggerProvider().Logger(scopeName)
	})
}

// RecordMatch records /match metrics and emits a single OTel summary
// log record (with at most maxLoggedMisses missed entries). It is purely
// additive to the existing stdout logging and never touches the response.
func RecordMatch(r *http.Request, results []*model.SingleResult, matchCount int, duration time.Duration, opts MatchOptions) {
	initInstruments()

	ctx := r.Context()

	clientType := "api"
	if r.Header.Get("X-CTSL-Client") == "frontend" {
		clientType = "frontend"
	}
	clientAttr := attribute.String("client_type", clientType)

	// Attributes attached to every per-request data point. classyfire_enabled
	// lets Grafana graph how many requests enable ClassyFire and their average
	// query count (match_queries_total / match_requests_total)
	requestSet := metric.WithAttributes(clientAttr,
		attribute.Bool("classyfire_enabled", opts.ClassyFireEnabled))

	queryCount := len(results)
	missCount := queryCount - matchCount
	durationMs := float64(duration.Microseconds()) / 1000.0

	var hitPercent float64
	if queryCount > 0 {
		hitPercent = float64(matchCount) / float64(queryCount) * 100.0
	}

	matchRequests.Add(ctx, 1, requestSet)
	matchQueries.Add(ctx, int64(queryCount), requestSet)
	matchMatches.Add(ctx, int64(matchCount), requestSet)
	matchHitPercent.Record(ctx, hitPercent, requestSet)
	matchDuration.Record(ctx, durationMs, requestSet)
	matchQueriesPerReq.Record(ctx, int64(queryCount), requestSet)

	// Collect the query type distribution and the first few misses in one pass.
	// The query type counter carries a "matched" attribute so the missed-query
	// distribution can be broken down by type (e.g. mostly malformed InChIKeys).
	misses := make([]log.Value, 0, maxLoggedMisses)
	for _, res := range results {
		matchQueryTypes.Add(ctx, 1, metric.WithAttributes(
			clientAttr,
			attribute.String("query_type", res.QueryType),
			attribute.Bool("matched", res.MatchFound),
		))

		if res.MatchFound || len(misses) >= maxLoggedMisses {
			continue
		}
		kvs := []log.KeyValue{
			log.String("query", res.Query),
			log.String("query_type", res.QueryType),
			log.String("error_message", res.ErrMsg),
		}
		if res.ConvertedQuery != "" {
			kvs = append(kvs, log.String("converted_query", res.ConvertedQuery))
		}
		misses = append(misses, log.MapValue(kvs...))
	}

	var record log.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(log.SeverityInfo)
	record.SetBody(log.StringValue("match summary"))
	record.AddAttributes(
		log.Int("match_count", matchCount),
		log.Int("query_count", queryCount),
		log.Int("miss_count", missCount),
		log.Float64("hit_percent", hitPercent),
		log.Float64("duration_ms", durationMs),
		log.String("client_type", clientType),
		log.Bool("top_hit_only", opts.TopHitOnly),
		log.Bool("first_block_matches", opts.AllowFirstBlockMatches),
		log.Bool("classyfire_enabled", opts.ClassyFireEnabled),
		log.Slice("misses", misses...),
	)
	if missCount > maxLoggedMisses {
		record.AddAttributes(log.Bool("misses_truncated", true))
	}
	matchLogger.Emit(ctx, record)
}

// RegisterClassyFireServiceGauge wires an observable gauge that samples
// ClassyFire reachability at each metric export: 1 when up, 0 when down
func RegisterClassyFireServiceGauge(up func() bool) {
	if up == nil {
		return
	}
	classyfireGaugeOnce.Do(func() {
		meter := otel.Meter(scopeName)
		_, err := meter.Int64ObservableGauge("classyfire_service_up",
			metric.WithDescription("ClassyFire service reachability sampled by the health probe (1 = up, 0 = down)"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				var v int64
				if up() {
					v = 1
				}
				o.Observe(v)
				return nil
			}),
		)
		if err != nil {
			otel.Handle(err)
		}
	})
}

// RecordClassyFireOutcomes counts terminal ClassyFire classification outcomes.
// Statuses: classified (successful), not_found (no classification exists),
// failed (unreachable, rate limit give-up, or circuit breaker)
func RecordClassyFireOutcomes(ctx context.Context, classified, notFound, failed int) {
	initInstruments()
	add := func(status string, n int) {
		if n <= 0 {
			return
		}
		classyfireClassifications.Add(ctx, int64(n),
			metric.WithAttributes(attribute.String("status", status)))
	}
	add("classified", classified)
	add("not_found", notFound)
	add("failed", failed)
}

// RegisterPubChemServiceGauge wires an observable gauge that samples the
// live PubChem fallback backend's reachability at each metric export: 1
// when up, 0 when down
func RegisterPubChemServiceGauge(up func() bool) {
	if up == nil {
		return
	}
	pubchemGaugeOnce.Do(func() {
		meter := otel.Meter(scopeName)
		_, err := meter.Int64ObservableGauge("pubchem_fallback_service_up",
			metric.WithDescription("PubChem fallback service reachability sampled by the health probe (1 = up, 0 = down)"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				var v int64
				if up() {
					v = 1
				}
				o.Observe(v)
				return nil
			}),
		)
		if err != nil {
			otel.Handle(err)
		}
	})
}

// RecordPubChemFallbackOutcomes counts terminal outcomes of live PubChem fallback for one query type
// Statuses: found , not_found, unparseable (RDKit could not read the structure),
// failed (no answer from PubChem), skipped (over the lookup limit, never attempted).
func RecordPubChemFallbackOutcomes(ctx context.Context, queryType string, found, notFound, unparseable, failed, skipped int) {
	initInstruments()
	add := func(status string, n int) {
		if n <= 0 {
			return
		}
		pubchemFallbackOutcomes.Add(ctx, int64(n), metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("query_type", queryType),
		))
	}
	add("found", found)
	add("not_found", notFound)
	add("unparseable", unparseable)
	add("failed", failed)
	add("skipped", skipped)
}

// RecordPubChemFallbackRequest records one HTTP round trip to PubChem: how
// long it took and how many values it carried.
// Result is ok, not_found, throttled, error, or aborted
func RecordPubChemFallbackRequest(ctx context.Context, endpoint, result string, duration time.Duration, batchSize int) {
	initInstruments()
	endpointAttr := attribute.String("endpoint", endpoint)
	pubchemFallbackDuration.Record(ctx, float64(duration.Microseconds())/1000.0,
		metric.WithAttributes(endpointAttr, attribute.String("result", result)))
	pubchemFallbackBatchSize.Record(ctx, int64(batchSize), metric.WithAttributes(endpointAttr))
}

// RecordPubChemFallbackValues counts the values one batch sent to an
// endpoint: found, not_found, or no_answer
func RecordPubChemFallbackValues(ctx context.Context, endpoint string, found, notFound, noAnswer int) {
	initInstruments()
	add := func(result string, n int) {
		if n <= 0 {
			return
		}
		pubchemFallbackValues.Add(ctx, int64(n), metric.WithAttributes(
			attribute.String("endpoint", endpoint),
			attribute.String("result", result),
		))
	}
	add("found", found)
	add("not_found", notFound)
	add("no_answer", noAnswer)
}

// pubchemComponent tags every PubChem fallback log record for Grafana to filter
const pubchemComponent = "pubchem_fallback"

// maxLoggedPathLen bounds the request path attached to an event, since a CID
// batch carries up to a thousand CIDs in its path
const maxLoggedPathLen = 200

// PubChemEvent is one notable step of a PubChem fallback call
type PubChemEvent struct {
	Event      string // retrying, gave_up, recovered, breaker_tripped, budget_exhausted
	Endpoint   string // cid, inchikey, fastformula
	Path       string // request path, trimmed of the property list
	Reason     string // throttled, rejected, http_error, transport_error, read_error, build_error
	StatusCode int
	Attempt    int
	Delay      time.Duration
	Err        error
}

// LogPubChemEvent emits a PubChemEvent as an OTel log record whose body is
// message. Severity follows the event: recovered is Info, retrying is Warn,
// and anything that ends a call without an answer is Error
func LogPubChemEvent(ctx context.Context, ev PubChemEvent, message string) {
	initInstruments()

	severity, severityText := log.SeverityWarn, "WARN"
	switch ev.Event {
	case "recovered":
		severity, severityText = log.SeverityInfo, "INFO"
	case "gave_up", "breaker_tripped", "budget_exhausted":
		severity, severityText = log.SeverityError, "ERROR"
	}

	var record log.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(severity)
	record.SetSeverityText(severityText)
	record.SetBody(log.StringValue(message))
	record.AddAttributes(
		log.String("component", pubchemComponent),
		log.String("event", ev.Event),
	)
	if ev.Endpoint != "" {
		record.AddAttributes(log.String("endpoint", ev.Endpoint))
	}
	if ev.Path != "" {
		path := ev.Path
		if len(path) > maxLoggedPathLen {
			path = path[:maxLoggedPathLen] + "..."
		}
		record.AddAttributes(log.String("path", path))
	}
	if ev.Reason != "" {
		record.AddAttributes(log.String("reason", ev.Reason))
	}
	if ev.StatusCode != 0 {
		record.AddAttributes(log.Int("status_code", ev.StatusCode))
	}
	if ev.Attempt != 0 {
		record.AddAttributes(log.Int("attempt", ev.Attempt))
	}
	if ev.Delay != 0 {
		record.AddAttributes(log.Float64("delay_ms", float64(ev.Delay.Milliseconds())))
	}
	if ev.Err != nil {
		record.AddAttributes(log.String("error", ev.Err.Error()))
	}
	matchLogger.Emit(ctx, record)
}

// PubChemFallbackSummary summarizes one /match request's PubChem fallback
type PubChemFallbackSummary struct {
	Misses          int // local misses handed to the fallback
	Attempted       int
	Found           int
	NotFound        int
	Unparseable     int
	Failed          int
	Skipped         int
	Calls           int // HTTP round trips to PubChem, retries included
	Retries         int
	BreakerTripped  bool
	BudgetExhausted bool
	Duration        time.Duration
}

// LogPubChemFallbackSummary emits one record per request that reached the fallback
func LogPubChemFallbackSummary(ctx context.Context, s PubChemFallbackSummary) {
	initInstruments()

	severity, severityText := log.SeverityInfo, "INFO"
	if s.BreakerTripped || s.BudgetExhausted {
		severity, severityText = log.SeverityWarn, "WARN"
	}

	var record log.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(severity)
	record.SetSeverityText(severityText)
	record.SetBody(log.StringValue("pubchem fallback summary"))
	record.AddAttributes(
		log.String("component", pubchemComponent),
		log.Int("misses", s.Misses),
		log.Int("attempted", s.Attempted),
		log.Int("found", s.Found),
		log.Int("not_found", s.NotFound),
		log.Int("unparseable", s.Unparseable),
		log.Int("failed", s.Failed),
		log.Int("skipped", s.Skipped),
		log.Int("calls", s.Calls),
		log.Int("retries", s.Retries),
		log.Bool("breaker_tripped", s.BreakerTripped),
		log.Bool("budget_exhausted", s.BudgetExhausted),
		log.Float64("duration_ms", float64(s.Duration.Microseconds())/1000.0),
	)
	matchLogger.Emit(ctx, record)
}

