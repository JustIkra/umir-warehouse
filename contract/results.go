package contract

import "time"

type CommitResultKind string

const (
	CommitResultOrderCommitted  CommitResultKind = "OrderCommitted"
	CommitResultRecheckRequired CommitResultKind = "RecheckRequired"
	CommitResultPending         CommitResultKind = "Pending"
)

type CommitResult struct {
	Kind       CommitResultKind
	Order      *Order
	Evaluation *OrderEvaluation
}

type OrderAttemptState string

const (
	OrderAttemptUnseen    OrderAttemptState = "Unseen"
	OrderAttemptPending   OrderAttemptState = "Pending"
	OrderAttemptCompleted OrderAttemptState = "Completed"
	OrderAttemptRejected  OrderAttemptState = "Rejected"
)

type ErrorOutcome string

const (
	OutcomeNotCommitted ErrorOutcome = "not_committed"
)

type OrderAttemptError struct {
	Code    string
	Message string
	Outcome ErrorOutcome
}

type OrderAttempt struct {
	State  OrderAttemptState
	Result *CommitResult
	Error  *OrderAttemptError
}

type DeliveryReceipt struct {
	DeliveryID string
	BatchIDs   []string
}

type ExpiryResult struct {
	BatchID            string
	ProductID          string
	WrittenOffQuantity Quantity
	Reason             string
	ExpiresAt          time.Time
	ProcessedAt        time.Time
}

type Menu struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Dishes       []Dish
}

type StopList struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Dishes       []Dish
	Deficits     []Change
}

type RecommendationReason struct {
	ProductID          string
	ProductName        string
	BatchID            string
	QuantityPerPortion Quantity
	BaseUnit           BaseUnit
	ExpiresAt          time.Time
}

type Recommendation struct {
	DishID            string
	DishName          string
	PriorityExpiresAt time.Time
	Reasons           []RecommendationReason
}

type Recommendations struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Items        []Recommendation
}

type CommandResultKind string

const (
	CommandResultProductRegistered CommandResultKind = "ProductRegistered"
	CommandResultDishDefined       CommandResultKind = "DishDefined"
	CommandResultDeliveryReceived  CommandResultKind = "DeliveryReceived"
	CommandResultOrderPlaced       CommandResultKind = "OrderPlaced"
	CommandResultBatchExpired      CommandResultKind = "BatchExpired"
)

type CommandResult struct {
	Kind     CommandResultKind
	Product  *Product
	Dish     *Dish
	Delivery *DeliveryReceipt
	Order    *Order
	Expiry   *ExpiryResult
}

type CommandRecord struct {
	CommandID          string
	CommandType        string
	CommandFingerprint string
	Result             CommandResult
}

type Products struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Items        []Product
}

type DishCatalog struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Items        []Dish
}

type DishDetails struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Dish         Dish
}

type ExpiringBatches struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Items        []ExpiringBatch
}

type History struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Events       []Event
}
