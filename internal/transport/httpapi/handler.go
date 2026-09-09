package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/enzom/jungle-gaming/internal/application"
)

type SQSReadiness interface {
	CheckInputQueue(context.Context) error
	CheckEventQueue(context.Context) error
}

type IdentityReadiness interface {
	Check(context.Context) error
}

type MetricsHandler interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

// Handler groups HTTP dependencies. Route registration, endpoint handlers,
// serialization and middleware live in their dedicated files.
type Handler struct {
	wallets  *application.WalletService
	wagers   *application.WagerService
	queries  *application.QueryService
	store    application.Store
	sqs      SQSReadiness
	identity IdentityReadiness
	metrics  MetricsHandler
	validate application.Validator
	log      *slog.Logger
}

func NewHandler(
	wallets *application.WalletService,
	wagers *application.WagerService,
	queries *application.QueryService,
	store application.Store,
	sqs SQSReadiness,
	identity IdentityReadiness,
	metrics MetricsHandler,
	validate application.Validator,
	log *slog.Logger,
) *Handler {
	return &Handler{
		wallets:  wallets,
		wagers:   wagers,
		queries:  queries,
		store:    store,
		sqs:      sqs,
		identity: identity,
		metrics:  metrics,
		validate: validate,
		log:      log,
	}
}
