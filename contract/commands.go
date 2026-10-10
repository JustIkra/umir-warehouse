package contract

import "time"

type RegisterProductRequest struct {
	CommandID string
	ProductID string
	Name      string
	BaseUnit  BaseUnit
}

type DefineDishRequest struct {
	CommandID   string
	DishID      string
	Name        string
	Ingredients []Ingredient
}

type ReceiveDeliveryRequest struct {
	CommandID  string
	DeliveryID string
	Number     string
	CreatedAt  time.Time
	Lines      []DeliveryLine
}

type EvaluateOrderRequest struct {
	Positions []Position
}

type CommitOrderRequest struct {
	CommandID         string
	ProposalID        string
	AcceptedPositions []AcceptedPosition
}

type ExpireBatchRequest struct {
	CommandID string
	BatchID   string
}
