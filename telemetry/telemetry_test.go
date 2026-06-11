package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
)

// TestSetupExportsAllSignals verifies the full export wiring: Setup reads
// OTEL_EXPORTER_OTLP_ENDPOINT, registers global providers, and on shutdown
// flushes one span, one metric, and one log record over OTLP/HTTP to the
// endpoint — here an httptest server standing in for the collector sidecar.
func TestSetupExportsAllSignals(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	fakeCollector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeCollector.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", fakeCollector.URL)

	// Setup overwrites the global providers; restore the ones installed by
	// TestMain so the instrumentation tests are unaffected by test order.
	prevTracer := otel.GetTracerProvider()
	prevMeter := otel.GetMeterProvider()
	prevLogger := logglobal.GetLoggerProvider()
	defer func() {
		otel.SetTracerProvider(prevTracer)
		otel.SetMeterProvider(prevMeter)
		logglobal.SetLoggerProvider(prevLogger)
	}()

	ctx := context.Background()
	shutdown, err := Setup(ctx)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Emit one of each signal through the global providers Setup registered.
	_, span := otel.Tracer("test").Start(ctx, "test-span")
	span.End()

	counter, err := otel.Meter("test").Int64Counter("test_counter")
	if err != nil {
		t.Fatalf("creating counter: %v", err)
	}
	counter.Add(ctx, 1)

	var rec otellog.Record
	rec.SetBody(otellog.StringValue("test log"))
	logglobal.GetLoggerProvider().Logger("test").Emit(ctx, rec)

	// Shutdown flushes all three pipelines to the endpoint.
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if hits[path] == 0 {
			t.Errorf("no export received on %s (hits: %v)", path, hits)
		}
	}
}
