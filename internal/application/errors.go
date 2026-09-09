package application

import "errors"

var (
	ErrNotFound                    = errors.New("resource not found")
	ErrConflict                    = errors.New("resource conflict")
	ErrIdempotencyPayloadConflict  = errors.New("idempotency key reused with different payload")
	ErrExternalTransactionConflict = errors.New("external transaction id reused")
	ErrInboxPayloadConflict        = errors.New("message id reused with different payload")
	ErrInvalidCursor               = errors.New("invalid pagination cursor")
)
