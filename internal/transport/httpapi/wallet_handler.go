package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/domain"
)

// createWallet godoc
//
//	@Summary		Open a wallet
//	@Description	Creates a wallet; requires the internal wallet-service role.
//	@Tags			Wallets
//	@Accept			json
//	@Produce		json
//	@Security		InternalOAuth
//	@Param			payload	body		CreateWalletRequest	true	"Wallet"
//	@Success		201		{object}	application.WalletView
//	@Failure		400		{object}	ProblemResponse
//	@Failure		401		{object}	ProblemResponse
//	@Failure		403		{object}	ProblemResponse
//	@Failure		409		{object}	ProblemResponse
//	@Failure		503		{object}	ProblemResponse
//	@Router			/wallets [post]
func (h *Handler) createWallet(w http.ResponseWriter, r *http.Request) {
	var request CreateWalletRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if err := h.validate.Struct(request); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	money, err := domain.ParseMoney(request.InitialBalance.Amount, request.InitialBalance.Currency)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.wallets.Create(r.Context(), application.CreateWalletCommand{
		PlayerID:       request.PlayerID,
		InitialBalance: money,
		CorrelationID:  correlationID(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// getWallet godoc
//
//	@Summary		Get a wallet
//	@Description	Requires the internal wallet-service role.
//	@Tags			Wallets
//	@Produce		json
//	@Security		InternalOAuth
//	@Param			walletId	path		string	true	"Wallet ID"	format(uuid)
//	@Success		200			{object}	application.WalletView
//	@Failure		400			{object}	ProblemResponse
//	@Failure		401			{object}	ProblemResponse
//	@Failure		403			{object}	ProblemResponse
//	@Failure		404			{object}	ProblemResponse
//	@Router			/wallets/{walletId} [get]
func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request) {
	walletID := chi.URLParam(r, "walletId")
	if h.validate.Var(walletID, "required,uuid") != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_ID", "walletId must be a UUID")
		return
	}
	result, err := h.wallets.Get(r.Context(), walletID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// getLedger godoc
//
//	@Summary		List a wallet ledger
//	@Description	Uses an opaque cursor and stable ordering; requires the internal role.
//	@Tags			Wallets
//	@Produce		json
//	@Security		InternalOAuth
//	@Param			walletId	path		string	true	"Wallet ID"	format(uuid)
//	@Param			cursor		query		string	false	"Opaque cursor"
//	@Param			limit		query		int		false	"Number of items (1-100)"	default(50)	minimum(1)	maximum(100)
//	@Success		200			{object}	application.LedgerPage
//	@Failure		400			{object}	ProblemResponse
//	@Failure		401			{object}	ProblemResponse
//	@Failure		403			{object}	ProblemResponse
//	@Failure		404			{object}	ProblemResponse
//	@Router			/wallets/{walletId}/ledger [get]
func (h *Handler) getLedger(w http.ResponseWriter, r *http.Request) {
	walletID := chi.URLParam(r, "walletId")
	if h.validate.Var(walletID, "required,uuid") != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_ID", "walletId must be a UUID")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be an integer")
			return
		}
		limit = parsed
	}
	query := struct {
		Limit int `validate:"gte=1,lte=100"`
	}{Limit: limit}
	if err := h.validate.Struct(query); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 100")
		return
	}
	result, err := h.wallets.Ledger(r.Context(), walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// reconcile godoc
//
//	@Summary		Reconcile wallet and ledger
//	@Description	Does not change the balance; requires the internal role.
//	@Tags			Wallets
//	@Produce		json
//	@Security		InternalOAuth
//	@Param			walletId	path		string	true	"Wallet ID"	format(uuid)
//	@Success		200			{object}	application.ReconciliationView
//	@Failure		400			{object}	ProblemResponse
//	@Failure		401			{object}	ProblemResponse
//	@Failure		403			{object}	ProblemResponse
//	@Failure		404			{object}	ProblemResponse
//	@Router			/wallets/{walletId}/reconciliation [post]
func (h *Handler) reconcile(w http.ResponseWriter, r *http.Request) {
	walletID := chi.URLParam(r, "walletId")
	if h.validate.Var(walletID, "required,uuid") != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_ID", "walletId must be a UUID")
		return
	}
	result, err := h.wallets.Reconcile(r.Context(), walletID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
