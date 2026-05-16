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

// InitTracerWithEndpoint 带有自定义 endpoint 的初始化函数
// endpoint 可通过环境变量 OTEL_EXPORTER_OTLP_ENDPOINT 覆盖
func InitTracerWithEndpoint(serviceName, mode, defaultEndpoint string) func(context.Context) error {
	// P0-01 fix: 支持通过参数传入默认 endpoint
	endpoint := defaultEndpoint
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		endpoint = ep
	}
	// R5-P1-01 fix: capture scheme before stripping — gRPC requires bare host:port
	useTLS := strings.HasPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	return initTracerCommon(serviceName, mode, endpoint, useTLS)
}

// InitTracer initialises the OpenTelemetry tracer provider and global propagator.
// It returns a shutdown function that MUST be called during graceful shutdown.
//
// P0-02 fix: endpoint is now configurable via OTEL_EXPORTER_OTLP_ENDPOINT env var
// (falls back to "localhost:4317"). Initialisation errors are returned instead of panic.
//
// P0-11 fix: sampling rate is configurable via OTEL_TRACE_SAMPLE_RATE env var
// (0.0–1.0, default 0.1 for non-dev). Use "1.0" or mode="dev" for full sampling.
func InitTracer(serviceName, mode string) func(context.Context) error {
	return InitTracerWithEndpoint(serviceName, mode, "localhost:4317")
}

// initTracerCommon 公共的 tracer 初始化逻辑
// R5-P1-01 fix: useTLS 参数独立传入，不再依赖 endpoint 中的 scheme 前缀
// gRPC exporter 需要裸 host:port，TLS 由 useTLS 布尔值控制
func initTracerCommon(serviceName, mode, endpoint string, useTLS bool) func(context.Context) error {
	// Use a bounded context so the exporter never blocks forever
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	// R5-P1-01 fix: 仅在非 TLS 时添加 WithInsecure()
	if !useTLS {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(
		initCtx,
		opts...,
	)
	if err != nil {
		// Return nil shutdown — tracer init is best-effort, don't block startup
		logger.Warnf("otel: failed to create exporter (endpoint=%s, tracing disabled): %v", endpoint, err)
		return func(ctx context.Context) error { return nil }
	}

	// Configurable sampling rate
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
