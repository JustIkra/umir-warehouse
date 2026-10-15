package domain

import "time"

type BaseUnit string

const (
	BaseUnitG   BaseUnit = "g"
	BaseUnitML  BaseUnit = "ml"
	BaseUnitPCS BaseUnit = "pcs"
)

type Product struct {
	ProductID string
	Name      string
	BaseUnit  BaseUnit
}

type Ingredient struct {
	ProductID string
	Quantity  Quantity
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
	OrganizationID, LifecycleVersion string
	DeliveryID                       string
	Number                           string
	CreatedAt                        time.Time
	Lines                            []DeliveryLine
}

type Batch struct {
	AppliedTimeZone        string
	AppliedSettingsVersion string
	TZDataVersion          string
	BatchID                string
	DeliveryID             string
	LineNo                 int64
	ProductID              string
	ManufacturedAt         time.Time
	ShelfLife              ShelfLife
	ReceivedAt             time.Time
	ExpiresAt              time.Time
	ReceivedQuantity       Quantity
	RemainingQuantity      Quantity
	ConsumedByOrders       Quantity
	PendingWriteoff        bool
	WrittenOffForExpiry    Quantity
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

type InventoryView struct {
	ProductID string
	BaseUnit  BaseUnit
	Quantity  Quantity
	Batches   []Batch
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

// InventoryState holds the current inventory. It keeps no order or command
// history: those live in the event store, which enforces their uniqueness.
type InventoryState struct {
	Products     map[string]Product
	Dishes       map[string]Dish
	DeliveryIDs  map[string]struct{}
	Batches      map[string]Batch
	StateVersion int64
}
