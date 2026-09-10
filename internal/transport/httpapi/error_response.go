package httpapi

import (
	"errors"
	"net/http"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/domain"
)

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, application.ErrIdempotencyPayloadConflict):
		writeProblem(w, http.StatusConflict, "IDEMPOTENCY_PAYLOAD_CONFLICT", err.Error())
	case errors.Is(err, application.ErrExternalTransactionConflict):
		writeProblem(w, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT", err.Error())
	case errors.Is(err, application.ErrConflict):
		writeProblem(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, application.ErrInvalidCursor):
		writeProblem(w, http.StatusBadRequest, "INVALID_CURSOR", err.Error())
	case errors.Is(err, domain.ErrInvalidMoney), errors.Is(err, domain.ErrInvalidWallet), errors.Is(err, domain.ErrInvalidTransaction):
		writeProblem(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	default:
		writeProblem(w, http.StatusServiceUnavailable, "TEMPORARY_UNAVAILABLE", "a required dependency is temporarily unavailable")
	}
}
