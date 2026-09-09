package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

type traceLogHandler struct {
	slog.Handler
}

func WithTraceContext(handler slog.Handler) slog.Handler {
	return &traceLogHandler{Handler: handler}
}

func (h *traceLogHandler) Handle(ctx context.Context, record slog.Record) error {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		record.AddAttrs(
			slog.String("traceId", spanContext.TraceID().String()),
			slog.String("spanId", spanContext.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h *traceLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceLogHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceLogHandler) WithGroup(name string) slog.Handler {
	return &traceLogHandler{Handler: h.Handler.WithGroup(name)}
}
