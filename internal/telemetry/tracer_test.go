package telemetry

import (
	"context"
	"testing"
)

func TestDisabledTracingUsesNoopProvider(t *testing.T) {
	provider, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.Tracer.(noopTracer); !ok {
		t.Fatalf("disabled tracing returned %T, want noopTracer", provider.Tracer)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownExporterIsRejected(t *testing.T) {
	_, err := New(context.Background(), Config{Enabled: true, Exporter: "otlp_typo"})
	if err == nil {
		t.Fatal("an unknown exporter was silently accepted")
	}
}

func TestStdoutExporterCreatesARealTracer(t *testing.T) {
	provider, err := New(context.Background(), Config{
		Enabled: true, Exporter: "stdout", ServiceName: "telemetry-test", ServiceVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.Tracer.(otelTracer); !ok {
		t.Fatalf("enabled tracing returned %T, want otelTracer", provider.Tracer)
	}
	_, span := provider.Tracer.Start(context.Background(), "test-span", "safe.key", "value", "dangling")
	span.SetString("result", "ok")
	span.End()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
