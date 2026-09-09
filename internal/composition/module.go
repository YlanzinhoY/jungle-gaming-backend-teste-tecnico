package composition

import (
	"log/slog"
	"net/http"
	"os"

	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/config"
	"github.com/enzom/jungle-gaming/internal/infrastructure/auth"
	"github.com/enzom/jungle-gaming/internal/infrastructure/messaging"
	"github.com/enzom/jungle-gaming/internal/infrastructure/observability"
	"github.com/enzom/jungle-gaming/internal/infrastructure/postgres"
	"github.com/enzom/jungle-gaming/internal/infrastructure/validation"
	"github.com/enzom/jungle-gaming/internal/transport/httpapi"
)

func Module() fx.Option {
	return fx.Module("wagering",
		fx.Module("foundation",
			fx.Provide(
				config.Load,
				newLogger,
				validation.New,
				observability.New,
				observability.NewTracing,
				func(metrics *observability.Metrics) application.Metrics { return metrics },
				func(metrics *observability.Metrics) httpapi.MetricsHandler {
					return metrics.Handler()
				},
			),
		),
		fx.Module("persistence",
			fx.Provide(
				postgres.NewPool,
				fx.Annotate(postgres.NewStore, fx.As(new(application.Store))),
			),
		),
		fx.Module("application",
			fx.Provide(
				provideReferencePolicy,
				application.NewWalletService,
				application.NewWagerService,
				application.NewQueryService,
			),
		),
		fx.Module("identity",
			fx.Provide(
				auth.New,
				func(authenticator *auth.Authenticator) httpapi.IdentityReadiness { return authenticator },
			),
		),
		fx.Module("messaging",
			fx.Provide(
				messaging.NewSQSClient,
				messaging.NewQueues,
				func(queues *messaging.Queues) httpapi.SQSReadiness { return queues },
			),
			fx.Invoke(
				messaging.RegisterConsumer,
				messaging.RegisterOutboxWorker,
				messaging.RegisterReferenceWorker,
			),
		),
		fx.Module("http",
			fx.Provide(httpapi.NewHandler, httpapi.NewServer),
			fx.Invoke(func(*http.Server) {}),
		),
	)
}

func newLogger() *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(observability.WithTraceContext(handler))
}

func provideReferencePolicy(cfg config.Config) application.ReferencePolicy {
	return application.ReferencePolicy{
		MaxAttempts: cfg.Workers.ReferenceMaxAttempts,
		BaseBackoff: cfg.Workers.ReferenceBaseBackoff,
		MaxBackoff:  cfg.Workers.ReferenceMaxBackoff,
	}
}
