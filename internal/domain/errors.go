package domain

import "errors"

var (
	ErrInvalidMoney          = errors.New("invalid money")
	ErrCurrencyMismatch      = errors.New("currency mismatch")
	ErrMoneyOverflow         = errors.New("money overflow")
	ErrInsufficientFunds     = errors.New("insufficient funds")
	ErrInvalidWallet         = errors.New("invalid wallet")
	ErrInvalidTransaction    = errors.New("invalid wager transaction")
	ErrInvalidTransition     = errors.New("invalid transaction state transition")
	ErrInvalidLedgerEntry    = errors.New("invalid ledger entry")
	ErrReferenceMismatch     = errors.New("reference does not match transaction")
	ErrReferenceNotProcessed = errors.New("reference transaction is not processed")
)
