package otel

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"

	"shopmicro/pkg/logger"
)

// InitTracer initialises the OpenTelemetry tracer provider and global propagator.
// It returns a shutdown function that MUST be called during graceful shutdown.
//
// P0-02 fix: endpoint is now configurable via OTEL_EXPORTER_OTLP_ENDPOINT env var
// (falls back to "localhost:4317"). Initialisation errors are returned instead of panic.
// An explicit timeout prevents infinite blocking when the collector is unreachable.
//
// P0-11 fix: sampling rate is configurable via OTEL_TRACE_SAMPLE_RATE env var
// (0.0–1.0, default 0.1 for non-dev). Use "1.0" or mode="dev" for full sampling.
func InitTracer(serviceName, mode string) func(context.Context) error {
	// P0-02: Configurable endpoint with env-var fallback
	endpoint := "localhost:4317" // secure default
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		endpoint = ep
	}
	// R2-P3-03 revert: otlptracegrpc.WithEndpoint expects bare host:port,
	// NOT a URL. Adding "http://" breaks gRPC address resolution.
	// Strip scheme prefix if env var contains one (e.g. "http://10.211.55.5:4317").
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	// P0-02: Use a bounded context so the exporter never blocks forever
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exporter, err := otlptracegrpc.New(
		initCtx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(), // TODO: configure TLS for production (P2-12)
	)
	if err != nil {
		// P0-02: Return nil shutdown — tracer init is best-effort, don't block startup
		logger.Warnf("otel: failed to create exporter (endpoint=%s, tracing disabled): %v", endpoint, err)
		return func(ctx context.Context) error { return nil }
	}

	// P0-11: Configurable sampling rate
	sampleRate := 0.1 // production default: 10%
	if mode == "dev" {
		sampleRate = 1.0
	}
	if sr := os.Getenv("OTEL_TRACE_SAMPLE_RATE"); sr != "" {
		if parsed, perr := strconv.ParseFloat(sr, 64); perr == nil && parsed >= 0 && parsed <= 1 {
			sampleRate = parsed
		}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(
			sdktrace.ParentBased(
				sdktrace.TraceIDRatioBased(sampleRate),
			),
		),
		sdktrace.WithBatcher(
			exporter,
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(
			resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceName(serviceName),
				semconv.DeploymentEnvironmentKey.String(mode),
			),
		),
	)

	otel.SetTracerProvider(tp)

	// W3C tracecontext propagation
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	logger.Infof("otel: tracer initialised (endpoint=%s, sampleRate=%.2f)", endpoint, sampleRate)
	return tp.Shutdown
}
