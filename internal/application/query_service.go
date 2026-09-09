package application

import "context"

type QueryService struct{ store Store }

func NewQueryService(store Store) *QueryService { return &QueryService{store: store} }

func (s *QueryService) Transaction(ctx context.Context, transactionID string) (TransactionView, error) {
	transaction, err := s.store.GetTransaction(ctx, transactionID)
	if err != nil {
		return TransactionView{}, err
	}
	return transactionView(transaction), nil
}

func (s *QueryService) ProviderTransaction(
	ctx context.Context,
	providerID, externalID string,
) (TransactionView, error) {
	transaction, err := s.store.GetProviderTransaction(ctx, providerID, externalID)
	if err != nil {
		return TransactionView{}, err
	}
	return transactionView(transaction), nil
}
