package observability

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/config"
)

type Tracing struct {
	provider   *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
}

func NewTracing(lifecycle fx.Lifecycle, cfg config.Config, log *slog.Logger) (*Tracing, error) {
	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	providerOptions := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.NeverSample()),
	}
	if cfg.Observability.TracingEnabled {
		exporterOptions := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Observability.OTLPEndpoint),
		}
		if cfg.Observability.OTLPInsecure {
			exporterOptions = append(exporterOptions, otlptracegrpc.WithInsecure())
		}
		exporter, err := otlptracegrpc.New(context.Background(), exporterOptions...)
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
		res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
			"",
			attribute.String("service.name", cfg.Observability.ServiceName),
			attribute.String("service.version", cfg.Observability.ServiceVersion),
			attribute.String("deployment.environment.name", cfg.Observability.Environment),
		))
		if err != nil {
			return nil, fmt.Errorf("create OpenTelemetry resource: %w", err)
		}
		providerOptions = []sdktrace.TracerProviderOption{
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Observability.TraceSample))),
		}
	}

	provider := sdktrace.NewTracerProvider(providerOptions...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagator)

	tracing := &Tracing{provider: provider, propagator: propagator}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			log.InfoContext(ctx, "OpenTelemetry tracing configured",
				"enabled", cfg.Observability.TracingEnabled,
				"serviceName", cfg.Observability.ServiceName,
				"otlpEndpoint", cfg.Observability.OTLPEndpoint,
				"sampleRatio", cfg.Observability.TraceSample,
			)
			return nil
		},
		OnStop: provider.Shutdown,
	})
	return tracing, nil
}

func (t *Tracing) TracerProvider() trace.TracerProvider {
	return t.provider
}

func (t *Tracing) Propagator() propagation.TextMapPropagator {
	return t.propagator
}
