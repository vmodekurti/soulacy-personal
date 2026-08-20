// Package telemetry provides OpenTelemetry tracing for Soulacy.
//
// Exporters: OTLP gRPC, OTLP HTTP, and stdout (for development). When tracing
// is disabled, a no-op tracer keeps instrumentation call sites branch-free.
package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const TracerName = "github.com/soulacy/soulacy"

// Config holds telemetry configuration.
type Config struct {
	Enabled        bool   // false = no-op tracer
	Exporter       string // "otlp_grpc" | "otlp_http" | "stdout" | "" (no-op)
	OTLPEndpoint   string // e.g. "localhost:4317" for gRPC, "http://localhost:4318" for HTTP
	ServiceName    string // default "soulacy"
	ServiceVersion string
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

// New initialises the tracer provider. Returns a no-op Provider when tracing
// is disabled or no exporter is configured. A configured-but-unknown exporter
// is an error: silently accepting a misspelling would tell an operator tracing
// is live while dropping every span.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled || cfg.Exporter == "" {
		return noopProvider(), nil
	}

	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	serviceName := strings.TrimSpace(cfg.ServiceName)
	if serviceName == "" {
		serviceName = "soulacy"
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(serviceName)}
	if version := strings.TrimSpace(cfg.ServiceVersion); version != "" {
		attrs = append(attrs, semconv.ServiceVersion(version))
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return &Provider{
		Tracer: otelTracer{tracer: tp.Tracer(TracerName)},
		stop:   tp.Shutdown,
	}, nil
}

func newExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch strings.TrimSpace(cfg.Exporter) {
	case "stdout":
		return stdouttrace.New()
	case "otlp_grpc":
		opts := grpcOptions(strings.TrimSpace(cfg.OTLPEndpoint))
		exporter, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("telemetry OTLP gRPC exporter: %w", err)
		}
		return exporter, nil
	case "otlp_http":
		opts := httpOptions(strings.TrimSpace(cfg.OTLPEndpoint))
		exporter, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("telemetry OTLP HTTP exporter: %w", err)
		}
		return exporter, nil
	default:
		return nil, fmt.Errorf("telemetry: unsupported exporter %q (want stdout, otlp_grpc, or otlp_http)", cfg.Exporter)
	}
}

// Bare host:port endpoints are the backwards-compatible local-development
// form documented by Soulacy and therefore use plaintext. A URL carries its
// own http/https choice. An empty endpoint delegates to OTEL environment
// variables and secure SDK defaults.
func grpcOptions(endpoint string) []otlptracegrpc.Option {
	if endpoint == "" {
		return nil
	}
	if strings.Contains(endpoint, "://") {
		return []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	}
	return []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure()}
}

func httpOptions(endpoint string) []otlptracehttp.Option {
	if endpoint == "" {
		return nil
	}
	if strings.Contains(endpoint, "://") {
		return []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint)}
	}
	return []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure()}
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

type otelTracer struct{ tracer trace.Tracer }

func (t otelTracer) Start(ctx context.Context, name string, kv ...string) (context.Context, Span) {
	attrs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		attrs = append(attrs, attribute.String(kv[i], kv[i+1]))
	}
	ctx, span := t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, otelSpan{span: span}
}

type otelSpan struct{ span trace.Span }

func (s otelSpan) End() { s.span.End() }
func (s otelSpan) SetString(key, value string) {
	s.span.SetAttributes(attribute.String(key, value))
}

// ---------------------------------------------------------------------------
// No-op implementations
// ---------------------------------------------------------------------------

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End()                  {}
func (noopSpan) SetString(_, _ string) {}
