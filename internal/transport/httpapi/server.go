package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/config"
	"github.com/enzom/jungle-gaming/internal/infrastructure/auth"
	"github.com/enzom/jungle-gaming/internal/infrastructure/observability"
)

func NewServer(
	lifecycle fx.Lifecycle,
	cfg config.Config,
	handler *Handler,
	authenticator *auth.Authenticator,
	tracing *observability.Tracing,
	log *slog.Logger,
) *http.Server {
	routes := handler.Routes(authenticator)
	server := &http.Server{
		Addr: cfg.HTTP.Address,
		Handler: otelhttp.NewHandler(routes, "http.server",
			otelhttp.WithTracerProvider(tracing.TracerProvider()),
			otelhttp.WithPropagators(tracing.Propagator()),
			otelhttp.WithFilter(func(request *http.Request) bool {
				return request.URL.Path != "/health/live" &&
					request.URL.Path != "/health/ready" &&
					request.URL.Path != "/metrics"
			}),
		),
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}
	var listener net.Listener
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) (err error) {
			listener, err = net.Listen("tcp", server.Addr)
			if err != nil {
				return err
			}
			go func() {
				log.Info("http server started", "address", server.Addr)
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server stopped unexpectedly", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(parent context.Context) error {
			ctx, cancel := context.WithTimeout(parent, cfg.HTTP.ShutdownTimeout)
			defer cancel()
			return server.Shutdown(ctx)
		},
	})
	return server
}
