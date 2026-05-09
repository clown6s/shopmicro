package middleware

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.opentelemetry.io/otel/sdk/resource"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

func InitTracer(serviceName string) func(context.Context) error {

	exporter, err := otlptracegrpc.New(
		context.Background(),

		otlptracegrpc.WithEndpoint(
			"10.211.55.5:4317",
		),

		otlptracegrpc.WithInsecure(),
	)

	if err != nil {
		panic(err)
	}

	tp := sdktrace.NewTracerProvider(

		sdktrace.WithBatcher(
			exporter,

			sdktrace.WithBatchTimeout(
				2*time.Second,
			),
		),

		sdktrace.WithSampler(
			sdktrace.AlwaysSample(),
		),

		sdktrace.WithResource(
			resource.NewWithAttributes(
				semconv.SchemaURL,

				semconv.ServiceName(
					serviceName,
				),
			),
		),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown
}
