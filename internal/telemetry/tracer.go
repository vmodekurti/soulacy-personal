// Package telemetry provides OpenTelemetry tracing for Soulacy.
//
// Exporters: OTLP gRPC (default), OTLP HTTP, stdout (for development).
// When OTLP is not configured, a no-op tracer is used so the code is
// always instrumented but incurs zero overhead without a collector.
//
// NOTE: To enable real OTEL tracing, add the following to go.mod via `go get`:
//
//	go.opentelemetry.io/otel v1.28.0
//	go.opentelemetry.io/otel/trace v1.28.0
//	go.opentelemetry.io/otel/sdk v1.28.0
//	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.28.0
//	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.28.0
//	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.28.0
//	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.28.0
//	go.opentelemetry.io/otel/semconv/v1.26.0 v1.26.0
//
// Until those deps are added, this file provides a zero-dep no-op implementation
// that satisfies the same interface so all call sites compile and run safely.
package telemetry

import (
	"context"
)

const TracerName = "github.com/soulacy/soulacy"

// Config holds telemetry configuration.
type Config struct {
	Enabled      bool   // false = no-op tracer
	Exporter     string // "otlp_grpc" | "otlp_http" | "stdout" | "" (no-op)
	OTLPEndpoint string // e.g. "localhost:4317" for gRPC, "http://localhost:4318" for HTTP
	ServiceName  string // default "soulacy"
}

// Span is a minimal tracing span that records key/value attributes.
// In the no-op implementation all methods are zero-overhead no-ops.
// Replace with go.opentelemetry.io/otel/trace.Span once OTEL deps are added.
type Span interface {
	// End marks the span as complete. Call with defer.
	End()
	// SetString records a string attribute on the span.
	SetString(key, value string)
}

// Tracer creates spans. The no-op implementation incurs no overhead.
// Replace with go.opentelemetry.io/otel/trace.Tracer once OTEL deps are added.
//
// The kv variadic is a flat list of string key/value pairs (key0, val0, …)
// so the interface can be satisfied by the runtime package's local
// telemetryTracer interface without any shared type imports.
type Tracer interface {
	// Start begins a new span named name. kv is an optional flat list of
	// string attribute key/value pairs. The returned context carries the span.
	Start(ctx context.Context, name string, kv ...string) (context.Context, Span)
}

// Provider wraps a Tracer and its shutdown function.
type Provider struct {
	Tracer Tracer
	stop   func(context.Context) error
}

// New initialises the tracer provider. Returns a no-op Provider when
// cfg.Enabled is false or Exporter is empty.
//
// Currently only the no-op implementation is available. To wire up a real
// OTEL exporter, add the OTEL deps listed at the top of this file and
// replace the switch cases below with the appropriate SDK constructors.
func New(_ context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled || cfg.Exporter == "" {
		return noopProvider(), nil
	}

	// TODO: once OTEL deps are added, replace these stubs with real exporters:
	//
	//   case "stdout":
	//       exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	//       ...
	//       tp = sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	//
	//   case "otlp_grpc":
	//       exp, err := otlptracegrpc.New(ctx,
	//           otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
	//           otlptracegrpc.WithInsecure(),
	//       )
	//       ...
	//
	//   case "otlp_http":
	//       exp, err := otlptracehttp.New(ctx,
	//           otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
	//           otlptracehttp.WithInsecure(),
	//       )
	//       ...
	//
	// And set the global tracer via: otel.SetTracerProvider(tp)
	//
	// Resource attributes to include:
	//   semconv.ServiceNameKey.String(serviceName),
	//   semconv.ServiceVersionKey.String(config.Version),

	// For now, any non-empty exporter value returns a no-op provider with a
	// warning-level comment so operators know tracing isn't active yet.
	_ = cfg.OTLPEndpoint // suppress unused warning
	return noopProvider(), nil
}

// Shutdown flushes pending spans and shuts down exporters.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.stop != nil {
		return p.stop(ctx)
	}
	return nil
}

// noopProvider returns a Provider backed entirely by no-op implementations.
func noopProvider() *Provider {
	return &Provider{
		Tracer: noopTracer{},
		stop:   func(_ context.Context) error { return nil },
	}
}

// ---------------------------------------------------------------------------
// No-op implementations
// ---------------------------------------------------------------------------

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End()                    {}
func (noopSpan) SetString(_, _ string)  {}
