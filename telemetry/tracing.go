package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan starts a span as a child of whatever span ctx carries
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(scopeName).Start(ctx, name, trace.WithAttributes(attrs...))
}

// AddSpanEvent records a timestamped event on the span carried by ctx
func AddSpanEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).AddEvent(name, trace.WithAttributes(attrs...))
}

// NewHTTPTransport wraps base so every outbound request gets its own client
// span named "<service> <METHOD>", parented to the span in the request's context
func NewHTTPTransport(base http.RoundTripper, service string) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
		return service + " " + r.Method
	}))
}
