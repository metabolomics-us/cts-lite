package telemetry

import (
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
	AllowRdkitConversion   bool
}

var (
	instrumentsOnce    sync.Once
	matchRequests      metric.Int64Counter
	matchQueries       metric.Int64Counter
	matchMatches       metric.Int64Counter
	matchQueryTypes    metric.Int64Counter
	matchHitPercent    metric.Float64Histogram
	matchDuration      metric.Float64Histogram
	matchQueriesPerReq metric.Int64Histogram
	matchLogger        log.Logger
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
	clientSet := metric.WithAttributes(clientAttr)

	queryCount := len(results)
	missCount := queryCount - matchCount
	durationMs := float64(duration.Microseconds()) / 1000.0

	var hitPercent float64
	if queryCount > 0 {
		hitPercent = float64(matchCount) / float64(queryCount) * 100.0
	}

	matchRequests.Add(ctx, 1, clientSet)
	matchQueries.Add(ctx, int64(queryCount), clientSet)
	matchMatches.Add(ctx, int64(matchCount), clientSet)
	matchHitPercent.Record(ctx, hitPercent, clientSet)
	matchDuration.Record(ctx, durationMs, clientSet)
	matchQueriesPerReq.Record(ctx, int64(queryCount), clientSet)

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
		log.Bool("rdkit_conversion", opts.AllowRdkitConversion),
		log.Slice("misses", misses...),
	)
	if missCount > maxLoggedMisses {
		record.AddAttributes(log.Bool("misses_truncated", true))
	}
	matchLogger.Emit(ctx, record)
}
