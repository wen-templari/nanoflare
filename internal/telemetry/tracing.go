package telemetry

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type TracingConfig struct {
	Endpoint    string
	ServiceName string
	SampleRatio float64
}

// ConfigureTracing installs an OTLP/gRPC tracer provider. An empty endpoint
// keeps tracing disabled so the server has no collector dependency by default.
func ConfigureTracing(ctx context.Context, config TracingConfig) (func(context.Context) error, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	if config.SampleRatio < 0 || config.SampleRatio > 1 {
		return nil, fmt.Errorf("OpenTelemetry sample ratio must be between 0 and 1")
	}
	serviceName := strings.TrimSpace(config.ServiceName)
	if serviceName == "" {
		serviceName = "nanoflared"
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithResource(resource.NewWithAttributes("",
			attribute.String("service.name", serviceName),
		)),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider.Shutdown, nil
}
