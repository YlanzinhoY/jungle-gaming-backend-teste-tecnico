package httpapi

type MoneyRequest struct {
	Amount   string `json:"amount" validate:"required" example:"25.00"`
	Currency string `json:"currency" validate:"required,iso4217" example:"BRL"`
}

type CreateWalletRequest struct {
	InitialBalance MoneyRequest `json:"initialBalance" validate:"required"`
	PlayerID       string       `json:"playerId" validate:"required,uuid" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
}

type WagerRequest struct {
	ExternalTransactionID          string       `json:"externalTransactionId" validate:"notblank,max=256" example:"transaction-123"`
	GameID                         string       `json:"gameId" validate:"notblank,max=256" example:"fortune-chimp"`
	Kind                           string       `json:"kind" validate:"required,oneof=BET WIN LOSS REFUND ROLLBACK" enums:"BET,WIN,LOSS,REFUND,ROLLBACK"`
	Money                          MoneyRequest `json:"money" validate:"required"`
	PlayerID                       string       `json:"playerId" validate:"required,uuid" example:"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"`
	ProviderID                     string       `json:"providerId" validate:"notblank,max=128" example:"provider-a"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId" validate:"required_if=Kind REFUND,required_if=Kind ROLLBACK,max=256"`
	RoundID                        string       `json:"roundId" validate:"notblank,max=256" example:"round-987"`
	WalletID                       string       `json:"walletId" validate:"required,uuid" example:"0192f291-27dd-7d3f-8071-5f8685deef37"`
}
