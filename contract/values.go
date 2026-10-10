package contract

import "time"

type Quantity int64
type BaseUnit string
type ShelfLifeUnit string

type ShelfLife struct {
	Value int64
	Unit  ShelfLifeUnit
}

const (
	BaseUnitG   BaseUnit = "g"
	BaseUnitML  BaseUnit = "ml"
	BaseUnitPCS BaseUnit = "pcs"

	ShelfLifeDay   ShelfLifeUnit = "day"
	ShelfLifeMonth ShelfLifeUnit = "month"
)

type Product struct {
	ProductID string
	Name      string
	BaseUnit  BaseUnit
}

type Ingredient struct {
	ProductID string
	Quantity  Quantity
	BaseUnit  BaseUnit
}

type Dish struct {
	DishID      string
	Name        string
	Ingredients []Ingredient
}

type DeliveryLine struct {
	ProductID      string
	ManufacturedAt time.Time
	ShelfLife      ShelfLife
	ReceivedAt     time.Time
	Quantity       Quantity
}

type Delivery struct {
	DeliveryID string
	Number     string
	CreatedAt  time.Time
	Lines      []DeliveryLine
}

type Batch struct {
	BatchID           string
	DeliveryID        string
	LineNo            int64
	ProductID         string
	ManufacturedAt    time.Time
	ShelfLife         ShelfLife
	ReceivedAt        time.Time
	ExpiresAt         time.Time
	ReceivedQuantity  Quantity
	RemainingQuantity Quantity
}

type Position struct {
	LineNo   int64
	DishID   string
	Portions int64
}

type AcceptedPosition struct {
	LineNo   int64
	DishID   string
	Portions int64
}

type Deficit struct {
	ProductID         string
	RequiredQuantity  Quantity
	AvailableQuantity Quantity
}

type Change struct {
	LineNo            int64
	DishID            string
	RequestedPortions int64
	AcceptedPortions  int64
	Deficits          []Deficit
}

type Allocation struct {
	ProductID string
	BatchID   string
	Quantity  Quantity
}

type Order struct {
	OrderID           string
	ProposalID        string
	AcceptedPositions []AcceptedPosition
	Allocations       []Allocation
	PlacedAt          time.Time
}

type OrderEvaluation struct {
	EvaluatedAt        time.Time
	StateVersion       int64
	RequestedPositions []Position
	AcceptedPositions  []AcceptedPosition
	Changes            []Change
}

type MenuEvaluation struct {
	EvaluatedAt       time.Time
	StateVersion      int64
	AvailableDishes   []Dish
	UnavailableDishes []Dish
	Deficits          []Change
}

type ProductInventory struct {
	ProductID string
	BaseUnit  BaseUnit
	Quantity  Quantity
	Batches   []Batch
}

type InventoryView struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Products     []ProductInventory
}

type ExpiringBatch struct {
	BatchID           string
	ProductID         string
	RemainingQuantity Quantity
	BaseUnit          BaseUnit
	ExpiresAt         time.Time
	RemainingSeconds  int64
	IsExpired         bool
}

type Event struct {
	EventID            string
	StreamID           string
	Sequence           int64
	SchemaVersion      int64
	CommandID          string
	CommandFingerprint string
	RecordedAt         time.Time
	Type               EventType
	Payload            EventPayload
}

type EventType string

const (
	EventProductRegistered EventType = "ProductRegistered"
	EventDishDefined       EventType = "DishDefined"
	EventDeliveryReceived  EventType = "DeliveryReceived"
	EventOrderPlaced       EventType = "OrderPlaced"
	EventBatchExpired      EventType = "BatchExpired"
)

type EventPayload struct {
	Product  *Product
	Dish     *Dish
	Delivery *Delivery
	Order    *Order
	Expiry   *ExpiryResult
}
