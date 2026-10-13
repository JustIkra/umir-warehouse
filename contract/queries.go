package contract

import "time"

type PrepareOrderRequest struct {
	Positions []Position
}

type ConfirmOrderRequest struct {
	CommandID  string
	ProposalID string
}

type GetExpiringBatchesRequest struct {
	CutoffAt time.Time
}

type StockPreparation struct {
	EvaluatedAt          time.Time
	StateVersion         int64
	InvalidationRequired bool
	Products             []Product
	Batches              []Batch
}
