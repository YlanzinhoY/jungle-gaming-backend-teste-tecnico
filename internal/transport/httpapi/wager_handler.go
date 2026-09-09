package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/domain"
	"github.com/enzom/jungle-gaming/internal/infrastructure/auth"
)

// processWager godoc
//
//	@Summary		Process a wager transaction
//	@Description	providerId must match the token provider_id claim. HTTP and SQS share the same idempotent use case.
//	@Tags			Wagering
//	@Accept			json
//	@Produce		json
//	@Security		ProviderOAuth
//	@Param			Idempotency-Key		header		string			true	"Idempotency key"
//	@Param			X-Correlation-ID	header		string			false	"Correlation ID"
//	@Param			payload				body		WagerRequest	true	"Transaction"
//	@Success		200					{object}	application.ProcessWagerResult
//	@Success		202					{object}	application.ProcessWagerResult	"Pending reference"
//	@Failure		400					{object}	ProblemResponse
//	@Failure		401					{object}	ProblemResponse
//	@Failure		403					{object}	ProblemResponse
//	@Failure		409					{object}	ProblemResponse
//	@Failure		422					{object}	application.ProcessWagerResult	"Auditable business rejection"
//	@Failure		500					{object}	application.ProcessWagerResult	"Auditable terminal technical failure"
//	@Failure		503					{object}	ProblemResponse
//	@Router			/wagering/transactions [post]
func (h *Handler) processWager(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.FromContext(r.Context())
	var request WagerRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if err := h.validate.Struct(request); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if principal.ProviderID == "" || principal.ProviderID != request.ProviderID {
		writeProblem(w, http.StatusForbidden, "PROVIDER_FORBIDDEN", "authenticated provider does not match providerId")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeProblem(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
		return
	}
	money, err := domain.ParseMoney(request.Money.Amount, request.Money.Currency)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.wagers.Process(r.Context(), application.ProcessWagerCommand{
		ProviderID:                     request.ProviderID,
		ExternalTransactionID:          request.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       request.PlayerID,
		WalletID:                       request.WalletID,
		RoundID:                        request.RoundID,
		GameID:                         request.GameID,
		Kind:                           domain.TransactionKind(request.Kind),
		Money:                          money,
		ReferenceExternalTransactionID: request.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if result.Status == domain.StatusPendingReference || result.Status == domain.StatusPending {
		status = http.StatusAccepted
	}
	if result.Status == domain.StatusRejected {
		status = http.StatusUnprocessableEntity
	}
	if result.Status == domain.StatusFailed {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, result)
}

// getTransaction godoc
//
//	@Summary		Get a transaction by internal ID
//	@Description	Requires the internal wallet-service role.
//	@Tags			Wagering
//	@Produce		json
//	@Security		InternalOAuth
//	@Param			transactionId	path		string	true	"Transaction ID"	format(uuid)
//	@Success		200				{object}	application.TransactionView
//	@Failure		400				{object}	ProblemResponse
//	@Failure		401				{object}	ProblemResponse
//	@Failure		403				{object}	ProblemResponse
//	@Failure		404				{object}	ProblemResponse
//	@Router			/wagering/transactions/{transactionId} [get]
func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	transactionID := chi.URLParam(r, "transactionId")
	if h.validate.Var(transactionID, "required,uuid") != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_ID", "transactionId must be a UUID")
		return
	}
	result, err := h.queries.Transaction(r.Context(), transactionID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// getProviderTransaction godoc
//
//	@Summary		Get an external transaction
//	@Description	A provider can query only its own transactions.
//	@Tags			Wagering
//	@Produce		json
//	@Security		ProviderOAuth
//	@Param			providerId				path		string	true	"Provider ID"
//	@Param			externalTransactionId	path		string	true	"External transaction ID"
//	@Success		200						{object}	application.TransactionView
//	@Failure		400						{object}	ProblemResponse
//	@Failure		401						{object}	ProblemResponse
//	@Failure		403						{object}	ProblemResponse
//	@Failure		404						{object}	ProblemResponse
//	@Router			/providers/{providerId}/wagering/transactions/{externalTransactionId} [get]
func (h *Handler) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.FromContext(r.Context())
	providerID := chi.URLParam(r, "providerId")
	externalID := chi.URLParam(r, "externalTransactionId")
	if h.validate.Var(providerID, "notblank,max=128") != nil || h.validate.Var(externalID, "notblank,max=256") != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_ID", "providerId or externalTransactionId is invalid")
		return
	}
	if principal.ProviderID == "" || principal.ProviderID != providerID {
		writeProblem(w, http.StatusForbidden, "PROVIDER_FORBIDDEN", "provider can only access its own transactions")
		return
	}
	result, err := h.queries.ProviderTransaction(r.Context(), providerID, externalID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
