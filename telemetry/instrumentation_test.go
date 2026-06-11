package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ctslite/model"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// captureProcessor is a minimal sdklog.Processor that stores every emitted
// log record so tests can assert on them.
type captureProcessor struct {
	records []sdklog.Record
}

func (p *captureProcessor) OnEmit(_ context.Context, r *sdklog.Record) error {
	p.records = append(p.records, r.Clone())
	return nil
}
func (p *captureProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (p *captureProcessor) Shutdown(context.Context) error                         { return nil }
func (p *captureProcessor) ForceFlush(context.Context) error                       { return nil }

func (p *captureProcessor) take() []sdklog.Record {
	r := p.records
	p.records = nil
	return r
}

var (
	testReader *sdkmetric.ManualReader
	capture    = &captureProcessor{}
)

// TestMain installs in-memory global providers BEFORE any test calls
// RecordMatch, because initInstruments binds instruments to the global
// providers exactly once (sync.Once). Delta temporality makes each
// Collect return only what was recorded since the previous Collect,
// isolating tests from each other.
func TestMain(m *testing.M) {
	testReader = sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality {
			return metricdata.DeltaTemporality
		}),
	)
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testReader)))
	logglobal.SetLoggerProvider(sdklog.NewLoggerProvider(sdklog.WithProcessor(capture)))
	os.Exit(m.Run())
}

// collectMetrics drains the manual reader and indexes the result by metric name.
func collectMetrics(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := testReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// sumValue returns the single int64 sum datapoint of a counter and its client_type.
func sumValue(t *testing.T, metrics map[string]metricdata.Metrics, name string) (int64, string) {
	t.Helper()
	m, ok := metrics[name]
	if !ok {
		t.Fatalf("metric %q not collected", name)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want Sum[int64]", name, m.Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("metric %q has %d datapoints, want 1", name, len(sum.DataPoints))
	}
	dp := sum.DataPoints[0]
	ct, _ := dp.Attributes.Value(attribute.Key("client_type"))
	return dp.Value, ct.AsString()
}

// logAttrs flattens a log record's attributes into a map for assertions.
func logAttrs(r sdklog.Record) map[string]otellog.Value {
	out := map[string]otellog.Value{}
	r.WalkAttributes(func(kv otellog.KeyValue) bool {
		out[kv.Key] = kv.Value
		return true
	})
	return out
}

// makeResults builds matched results (pubchem_id) and missed results (smiles).
func makeResults(matched, missed int) []*model.SingleResult {
	results := make([]*model.SingleResult, 0, matched+missed)
	for i := 0; i < matched; i++ {
		results = append(results, &model.SingleResult{
			Query:      fmt.Sprintf("%d", 1000+i),
			QueryType:  "pubchem_id",
			MatchFound: true,
		})
	}
	for i := 0; i < missed; i++ {
		results = append(results, &model.SingleResult{
			Query:      fmt.Sprintf("MISS%d", i),
			QueryType:  "smiles",
			MatchFound: false,
			ErrMsg:     "No compound found",
		})
	}
	return results
}

func newMatchRequest(frontend bool) *http.Request {
	r := httptest.NewRequest("POST", "/match", nil)
	if frontend {
		r.Header.Set("X-CTSL-Client", "frontend")
	}
	return r
}

func TestRecordMatchMetrics(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(false), makeResults(3, 2), 3, 250*time.Millisecond, MatchOptions{})
	metrics := collectMetrics(t)

	if v, ct := sumValue(t, metrics, "match_requests_total"); v != 1 || ct != "api" {
		t.Errorf("match_requests_total = %d (client_type=%q), want 1 (api)", v, ct)
	}
	if v, _ := sumValue(t, metrics, "match_queries_total"); v != 5 {
		t.Errorf("match_queries_total = %d, want 5", v)
	}
	if v, _ := sumValue(t, metrics, "match_matches_total"); v != 3 {
		t.Errorf("match_matches_total = %d, want 3", v)
	}

	hist, ok := metrics["match_hit_percent"].Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("match_hit_percent: unexpected data %#v", metrics["match_hit_percent"].Data)
	}
	if sum := hist.DataPoints[0].Sum; sum != 60.0 {
		t.Errorf("match_hit_percent sum = %v, want 60.0 (3 of 5)", sum)
	}

	dur, ok := metrics["match_duration_ms"].Data.(metricdata.Histogram[float64])
	if !ok || len(dur.DataPoints) != 1 {
		t.Fatalf("match_duration_ms: unexpected data %#v", metrics["match_duration_ms"].Data)
	}
	if sum := dur.DataPoints[0].Sum; sum != 250.0 {
		t.Errorf("match_duration_ms sum = %v, want 250.0", sum)
	}
}

func TestRecordMatchQueriesPerRequestBuckets(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(false), makeResults(4, 1), 4, time.Millisecond, MatchOptions{})
	metrics := collectMetrics(t)

	hist, ok := metrics["match_queries_per_request"].Data.(metricdata.Histogram[int64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("match_queries_per_request: unexpected data %#v", metrics["match_queries_per_request"].Data)
	}
	dp := hist.DataPoints[0]

	wantBounds := []float64{1, 5, 50, 250, 1000, 5000, 25000, 100000}
	if len(dp.Bounds) != len(wantBounds) {
		t.Fatalf("bounds = %v, want %v", dp.Bounds, wantBounds)
	}
	for i, b := range wantBounds {
		if dp.Bounds[i] != b {
			t.Fatalf("bounds = %v, want %v", dp.Bounds, wantBounds)
		}
	}
	// 5 queries falls in the (1, 5] bucket, which is index 1.
	if dp.BucketCounts[1] != 1 {
		t.Errorf("bucket counts = %v, want the (1,5] bucket (index 1) to hold 1", dp.BucketCounts)
	}
	if dp.Count != 1 || dp.Sum != 5 {
		t.Errorf("count = %d, sum = %d, want count 1 sum 5", dp.Count, dp.Sum)
	}
}

func TestRecordMatchClientTypeFrontend(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(true), makeResults(1, 0), 1, time.Millisecond, MatchOptions{})
	metrics := collectMetrics(t)

	if _, ct := sumValue(t, metrics, "match_requests_total"); ct != "frontend" {
		t.Errorf("client_type = %q, want frontend", ct)
	}

	records := capture.take()
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	if ct := logAttrs(records[0])["client_type"]; ct.AsString() != "frontend" {
		t.Errorf("log client_type = %q, want frontend", ct.AsString())
	}
}

func TestRecordMatchQueryTypeBreakdown(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(false), makeResults(3, 2), 3, time.Millisecond, MatchOptions{})
	metrics := collectMetrics(t)

	sum, ok := metrics["match_query_type_total"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("match_query_type_total: unexpected data %#v", metrics["match_query_type_total"].Data)
	}
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		qt, _ := dp.Attributes.Value(attribute.Key("query_type"))
		matched, _ := dp.Attributes.Value(attribute.Key("matched"))
		got[fmt.Sprintf("%s/%v", qt.AsString(), matched.AsBool())] = dp.Value
	}
	if got["pubchem_id/true"] != 3 {
		t.Errorf("pubchem_id matched = %d, want 3", got["pubchem_id/true"])
	}
	if got["smiles/false"] != 2 {
		t.Errorf("smiles missed = %d, want 2", got["smiles/false"])
	}
}

func TestRecordMatchLogSummaryTruncatesMisses(t *testing.T) {
	capture.take()
	results := makeResults(3, 7)                     // 7 misses: over the cap of 5
	results[3].ConvertedQuery = "CONVERTED-INCHIKEY" // first miss carries a conversion
	opts := MatchOptions{TopHitOnly: true, AllowFirstBlockMatches: false, AllowRdkitConversion: true, ClassyFireEnabled: false}
	RecordMatch(newMatchRequest(false), results, 3, 100*time.Millisecond, opts)
	collectMetrics(t) // drain metrics so later tests stay isolated

	records := capture.take()
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	rec := records[0]
	if body := rec.Body().AsString(); body != "match summary" {
		t.Errorf("body = %q, want \"match summary\"", body)
	}
	if rec.Severity() != otellog.SeverityInfo {
		t.Errorf("severity = %v, want Info", rec.Severity())
	}

	attrs := logAttrs(rec)
	if v := attrs["match_count"].AsInt64(); v != 3 {
		t.Errorf("match_count = %d, want 3", v)
	}
	if v := attrs["query_count"].AsInt64(); v != 10 {
		t.Errorf("query_count = %d, want 10", v)
	}
	if v := attrs["miss_count"].AsInt64(); v != 7 {
		t.Errorf("miss_count = %d, want 7", v)
	}
	if v := attrs["hit_percent"].AsFloat64(); v != 30.0 {
		t.Errorf("hit_percent = %v, want 30.0", v)
	}
	if !attrs["top_hit_only"].AsBool() || attrs["first_block_matches"].AsBool() || !attrs["rdkit_conversion"].AsBool() {
		t.Errorf("options not recorded correctly: %v", attrs)
	}
	if v, ok := attrs["misses_truncated"]; !ok || !v.AsBool() {
		t.Error("misses_truncated missing or false, want true")
	}

	misses := attrs["misses"].AsSlice()
	if len(misses) != maxLoggedMisses {
		t.Fatalf("misses length = %d, want %d", len(misses), maxLoggedMisses)
	}
	first := map[string]otellog.Value{}
	for _, kv := range misses[0].AsMap() {
		first[kv.Key] = kv.Value
	}
	if first["query"].AsString() != "MISS0" || first["query_type"].AsString() != "smiles" {
		t.Errorf("first miss = %v, want query MISS0 / smiles", first)
	}
	if first["converted_query"].AsString() != "CONVERTED-INCHIKEY" {
		t.Errorf("first miss converted_query = %v, want CONVERTED-INCHIKEY", first["converted_query"])
	}
	second := map[string]otellog.Value{}
	for _, kv := range misses[1].AsMap() {
		second[kv.Key] = kv.Value
	}
	if _, ok := second["converted_query"]; ok {
		t.Error("second miss has converted_query, want absent")
	}
}

func TestRecordMatchNoTruncationUnderCap(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(false), makeResults(1, 2), 1, time.Millisecond, MatchOptions{})
	collectMetrics(t)

	records := capture.take()
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	attrs := logAttrs(records[0])
	if len(attrs["misses"].AsSlice()) != 2 {
		t.Errorf("misses length = %d, want 2", len(attrs["misses"].AsSlice()))
	}
	if _, ok := attrs["misses_truncated"]; ok {
		t.Error("misses_truncated present, want absent when under the cap")
	}
}

// The per-request metrics needed for the ClassyFire Grafana panels carry the
// classyfire_enabled attribute so requests and query counts can be filtered
func TestRecordMatchClassyFireEnabledAttribute(t *testing.T) {
	capture.take()
	RecordMatch(newMatchRequest(false), makeResults(2, 1), 2, time.Millisecond, MatchOptions{ClassyFireEnabled: true})
	metrics := collectMetrics(t)

	for _, name := range []string{"match_requests_total", "match_queries_total"} {
		sum, ok := metrics[name].Data.(metricdata.Sum[int64])
		if !ok || len(sum.DataPoints) != 1 {
			t.Fatalf("%s: unexpected data %#v", name, metrics[name].Data)
		}
		cf, ok := sum.DataPoints[0].Attributes.Value(attribute.Key("classyfire_enabled"))
		if !ok || !cf.AsBool() {
			t.Errorf("%s: classyfire_enabled attribute = %v (present=%v), want true", name, cf.AsBool(), ok)
		}
	}

	hist, ok := metrics["match_queries_per_request"].Data.(metricdata.Histogram[int64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("match_queries_per_request: unexpected data %#v", metrics["match_queries_per_request"].Data)
	}
	cf, ok := hist.DataPoints[0].Attributes.Value(attribute.Key("classyfire_enabled"))
	if !ok || !cf.AsBool() {
		t.Errorf("match_queries_per_request: classyfire_enabled = %v (present=%v), want true", cf.AsBool(), ok)
	}

	records := capture.take()
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	if !logAttrs(records[0])["classyfire_enabled"].AsBool() {
		t.Error("log classyfire_enabled = false, want true")
	}
}

func TestRecordClassyFireOutcomes(t *testing.T) {
	capture.take()
	RecordClassyFireOutcomes(context.Background(), 3, 1, 2)
	metrics := collectMetrics(t)

	sum, ok := metrics["classyfire_classifications_total"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("classyfire_classifications_total: unexpected data %#v", metrics["classyfire_classifications_total"].Data)
	}
	got := map[string]int64{}
	for _, dp := range sum.DataPoints {
		status, _ := dp.Attributes.Value(attribute.Key("status"))
		got[status.AsString()] = dp.Value
	}
	want := map[string]int64{"classified": 3, "not_found": 1, "failed": 2}
	for status, n := range want {
		if got[status] != n {
			t.Errorf("status %q = %d, want %d", status, got[status], n)
		}
	}
}

// Zero counts must not emit datapoints, so absent statuses stay absent in Grafana
func TestRecordClassyFireOutcomesSkipsZeroes(t *testing.T) {
	capture.take()
	RecordClassyFireOutcomes(context.Background(), 2, 0, 0)
	metrics := collectMetrics(t)

	sum, ok := metrics["classyfire_classifications_total"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("classyfire_classifications_total: unexpected data %#v", metrics["classyfire_classifications_total"].Data)
	}
	if len(sum.DataPoints) != 1 {
		t.Fatalf("got %d datapoints, want 1 (zero counts must not record)", len(sum.DataPoints))
	}
	status, _ := sum.DataPoints[0].Attributes.Value(attribute.Key("status"))
	if status.AsString() != "classified" || sum.DataPoints[0].Value != 2 {
		t.Errorf("datapoint = %s/%d, want classified/2", status.AsString(), sum.DataPoints[0].Value)
	}
}

func TestRecordMatchEmptyResults(t *testing.T) {
	capture.take()
	// Must not panic or divide by zero.
	RecordMatch(newMatchRequest(false), nil, 0, time.Millisecond, MatchOptions{})
	collectMetrics(t)

	records := capture.take()
	if len(records) != 1 {
		t.Fatalf("got %d log records, want 1", len(records))
	}
	attrs := logAttrs(records[0])
	if v := attrs["query_count"].AsInt64(); v != 0 {
		t.Errorf("query_count = %d, want 0", v)
	}
	if v := attrs["hit_percent"].AsFloat64(); v != 0 {
		t.Errorf("hit_percent = %v, want 0", v)
	}
}
