package otel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"shopmicro/pkg/logger"
)

// InitTracerWithEndpoint 带有自定义 endpoint 的初始化函数
// endpoint 可通过环境变量 OTEL_EXPORTER_OTLP_ENDPOINT 覆盖
// 返回 (shutdown 函数, error)，调用方必须检查 error
func InitTracerWithEndpoint(serviceName, mode, defaultEndpoint string) (func(context.Context) error, error) {
	// 环境变量优先级：OTEL_EXPORTER_OTLP_ENDPOINT=none 表示显式禁用 otel
	// 空字符串也表示禁用（Docker 部署场景，不部署 collector）
	envEp := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if envEp == "" || envEp == "none" || envEp == "off" {
		logger.Info("otel: disabled (OTEL_EXPORTER_OTLP_ENDPOINT not set or empty)")
		return func(ctx context.Context) error { return nil }, nil
	}
	endpoint := envEp
	// R5-P1-01 修复：在去除前缀前捕获 scheme — gRPC 需要裸 host:port
	useTLS := strings.HasPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")

	return initTracerCommon(serviceName, mode, endpoint, useTLS)
}

// InitTracer 初始化 OpenTelemetry tracer provider 和全局传播器。
// 返回一个关闭函数和 error，必须在优雅关闭期间调用关闭函数。
//
// P0-02 修复：endpoint 现在可通过 OTEL_EXPORTER_OTLP_ENDPOINT 环境变量配置
// （回退到 "localhost:4317"）。初始化错误会被返回而不是 panic。
//
// P0-11 修复：采样率可通过 OTEL_TRACE_SAMPLE_RATE 环境变量配置
// （0.0–1.0，非 dev 模式默认 0.1）。使用 "1.0" 或 mode="dev" 进行全量采样。
func InitTracer(serviceName, mode string) (func(context.Context) error, error) {
	return InitTracerWithEndpoint(serviceName, mode, "localhost:4317")
}

// initTracerCommon 公共的 tracer 初始化逻辑
// R5-P1-01 修复：useTLS 参数独立传入，不再依赖 endpoint 中的 scheme 前缀
// gRPC exporter 需要裸 host:port，TLS 由 useTLS 布尔值控制
func initTracerCommon(serviceName, mode, endpoint string, useTLS bool) (func(context.Context) error, error) {
	// 使用有超时的上下文，防止 exporter 永久阻塞
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	// R5-P1-01 修复：仅在非 TLS 时添加 WithInsecure()
	if !useTLS {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(
		initCtx,
		opts...,
	)
	if err != nil {
		if mode == "dev" {
			// dev 模式：允许无 tracing 降级启动，返回 noop shutdown
			logger.Infof("otel: exporter unavailable in dev mode (endpoint=%s), tracing disabled: %v", endpoint, err)
			return func(ctx context.Context) error { return nil }, nil
		}
		// prod/staging 模式：返回错误，由调用方决定是否终止启动
		logger.Errorf("otel: failed to create exporter (endpoint=%s): %v", endpoint, err)
		return nil, fmt.Errorf("otel: failed to create exporter (endpoint=%s): %w", endpoint, err)
	}

	// 可配置的采样率
	sampleRate := 0.1 // 生产环境默认：10%
	if mode == "dev" {
		sampleRate = 1.0
	}
	if sr := os.Getenv("OTEL_TRACE_SAMPLE_RATE"); sr != "" {
		if parsed, perr := strconv.ParseFloat(sr, 64); perr == nil && parsed >= 0 && parsed <= 1 {
			sampleRate = parsed
		} else {
			logger.Warnf("otel: invalid OTEL_TRACE_SAMPLE_RATE=%q, using default %.2f", sr, sampleRate)
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

	// W3C tracecontext 传播
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	logger.Infof("otel: tracer initialised (endpoint=%s, sampleRate=%.2f)", endpoint, sampleRate)
	return tp.Shutdown, nil
}
