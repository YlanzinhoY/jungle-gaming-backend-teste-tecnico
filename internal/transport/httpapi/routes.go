package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger/v2"

	"github.com/enzom/jungle-gaming/internal/infrastructure/auth"
)

func (h *Handler) Routes(authenticator *auth.Authenticator) http.Handler {
	router := chi.NewRouter()
	router.Use(h.requestContext, h.requestLogger, h.recoverer)
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusMethodNotAllowed, "ROUTE_METHOD_NOT_ALLOWED", "HTTP method is not allowed for this route")
	})
	router.Get("/health/live", h.live)
	router.Get("/health/ready", h.ready)
	router.Handle("/metrics", h.metrics)
	router.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusPermanentRedirect)
	})
	router.Handle("/swagger/*", httpSwagger.Handler(
		httpSwagger.AfterScript("ui.initOAuth({appName: 'Jungle Gaming local', usePkceWithAuthorizationCodeGrant: true, additionalQueryStringParams: {prompt: 'login'}})"),
	))
	router.Group(func(protected chi.Router) {
		protected.Use(authenticator.Middleware)
		protected.Post("/wagering/transactions", h.processWager)
		protected.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", h.getProviderTransaction)
		protected.Group(func(internal chi.Router) {
			internal.Use(authenticator.RequireInternal)
			internal.Post("/wallets", h.createWallet)
			internal.Get("/wallets/{walletId}", h.getWallet)
			internal.Get("/wallets/{walletId}/ledger", h.getLedger)
			internal.Post("/wallets/{walletId}/reconciliation", h.reconcile)
			internal.Get("/wagering/transactions/{transactionId}", h.getTransaction)
		})
	})
	return router
}
