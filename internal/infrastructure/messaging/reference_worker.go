package messaging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/config"
)

func RegisterReferenceWorker(
	lifecycle fx.Lifecycle,
	store application.Store,
	service *application.WagerService,
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
				resolveReferences(ctx, store, service, cfg, log)
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

func resolveReferences(
	ctx context.Context,
	store application.Store,
	service *application.WagerService,
	cfg config.Config,
	log *slog.Logger,
) {
	for ctx.Err() == nil {
		ids, err := store.FindDuePendingReferences(ctx, cfg.Workers.ReferenceBatchSize, time.Now().UTC())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.ErrorContext(ctx, "pending reference lookup failed", "error", err)
			waitContext(ctx, cfg.Workers.PollInterval)
			continue
		}
		if len(ids) == 0 {
			waitContext(ctx, cfg.Workers.PollInterval)
			continue
		}
		for _, id := range ids {
			if err := service.ResolvePendingReference(ctx, id); err != nil && ctx.Err() == nil {
				log.ErrorContext(
					ctx,
					"pending reference resolution failed",
					"transactionId", id,
					"error", err,
				)
				if recordErr := service.RecordPendingReferenceFailure(ctx, id); recordErr != nil && ctx.Err() == nil {
					log.ErrorContext(
						ctx,
						"pending reference failure could not be recorded",
						"transactionId", id,
						"error", recordErr,
					)
				}
			}
		}
	}
}
