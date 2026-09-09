package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/config"
	"github.com/enzom/jungle-gaming/internal/infrastructure/observability"
)

func NewPool(
	lifecycle fx.Lifecycle,
	cfg config.Config,
	tracing *observability.Tracing,
	log *slog.Logger,
) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = cfg.Database.MaxConnections
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithTracerProvider(tracing.TracerProvider()))
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("database readiness: %w", err)
			}
			log.InfoContext(ctx, "postgres connected")
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
