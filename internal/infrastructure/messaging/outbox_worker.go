package messaging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/config"
)

func RegisterOutboxWorker(
	lifecycle fx.Lifecycle,
	store application.Store,
	queues *Queues,
	metrics application.Metrics,
	cfg config.Config,
	log *slog.Logger,
) {
	if !cfg.Workers.Enabled {
		return
	}
	var cancel context.CancelFunc
	var worker sync.WaitGroup
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			worker.Add(1)
			go func() {
				defer worker.Done()
				publishOutbox(ctx, store, queues, metrics, cfg, log)
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel != nil {
				cancel()
			}
			done := make(chan struct{})
			go func() {
				worker.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}

func publishOutbox(
	ctx context.Context,
	store application.Store,
	queues *Queues,
	metrics application.Metrics,
	cfg config.Config,
	log *slog.Logger,
) {
	workerID := uuid.NewString()
	for ctx.Err() == nil {
		now := time.Now().UTC()
		events, err := store.ClaimOutbox(ctx, workerID, cfg.Workers.OutboxBatchSize, cfg.Workers.OutboxLease, now)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.ErrorContext(ctx, "outbox claim failed", "workerId", workerID, "error", err)
			waitContext(ctx, cfg.Workers.PollInterval)
			continue
		}
		if len(events) == 0 {
			waitContext(ctx, cfg.Workers.PollInterval)
			continue
		}
		for _, event := range events {
			eventCtx, span := otel.Tracer("github.com/enzom/jungle-gaming/messaging").Start(
				ctx,
				"SQS publish "+event.EventType,
				trace.WithSpanKind(trace.SpanKindProducer),
				trace.WithAttributes(
					attribute.String("messaging.system", "aws_sqs"),
					attribute.String("messaging.operation.name", "publish"),
					attribute.String("app.event_id", event.EventID),
					attribute.String("app.aggregate_id", event.AggregateID),
					attribute.String("app.event_type", event.EventType),
				),
			)
			if err := queues.publish(eventCtx, event.EventID, event.AggregateID, event.Payload); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "outbox publish failed")
				span.End()
				delay := outboxBackoff(event.Attempts + 1)
				_ = store.ReleaseOutbox(ctx, event.EventID, workerID, time.Now().UTC().Add(delay), err.Error())
				log.ErrorContext(eventCtx, "outbox publish failed", "eventId", event.EventID, "eventType", event.EventType, "error", err)
				continue
			}
			if err := store.MarkOutboxPublished(eventCtx, event.EventID, workerID, time.Now().UTC()); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "outbox confirmation failed")
				span.End()
				log.ErrorContext(
					eventCtx,
					"outbox confirmation failed; event may be republished",
					"eventId", event.EventID,
					"error", err,
				)
				continue
			}
			span.End()
			metrics.ObserveOutboxLag(time.Since(event.OccurredAt))
		}
	}
}

func outboxBackoff(attempt int) time.Duration {
	delay := time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
