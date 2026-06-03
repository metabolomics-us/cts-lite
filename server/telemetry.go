package main

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// setupTelemetry wires up the global OpenTelemetry trace, metric, and log
// providers using OTLP/HTTP exporters. Endpoint, headers, service name, and
// resource attributes are all read from the standard OTEL_* environment
// variables by the exporters and the SDK, so there is nothing to configure in
// code. With OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 the data is sent
// to the collector sidecar.
//
// It returns a shutdown function that flushes and closes every provider. The
// caller should always call it (errors are joined), even when setup partially
// failed: providers that were created are still registered globally.
func setupTelemetry(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	shutdown = func(ctx context.Context) error {
		var errs error
		for _, fn := range shutdownFuncs {
			errs = errors.Join(errs, fn(ctx))
		}
		shutdownFuncs = nil
		return errs
	}

	// Propagate W3C trace context so spans stitch together across services
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Traces
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return shutdown, err
	}
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter))
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Metrics
	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return shutdown, err
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	// Logs
	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		return shutdown, err
	}
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	logglobal.SetLoggerProvider(loggerProvider)

	return shutdown, nil
}
