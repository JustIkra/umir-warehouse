package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

func EventSchemaVersion(event Event) int64 {
	if event.Type == EventBatchExpiryPending || event.Type == EventBatchWriteoffConfirmed || event.Type == EventDeliveryReceived && event.TenantID != "" {
		return InventorySchemaV2
	}
	return InventorySchemaV1
}

const (
	InventoryStreamID      = "inventory"
	InventorySchemaV1      = int64(1)
	InventorySchemaV2      = int64(2)
	ExpiryReasonExpiration = "EXPIRATION"
)

type EventType string

const (
	EventProductRegistered      EventType = "ProductRegistered"
	EventDishDefined            EventType = "DishDefined"
	EventDeliveryReceived       EventType = "DeliveryReceived"
	EventOrderPlaced            EventType = "OrderPlaced"
	EventBatchExpiryPending     EventType = "BatchExpiryPending"
	EventBatchWriteoffConfirmed EventType = "BatchWriteoffConfirmed"
	EventBatchExpired           EventType = "BatchExpired"
)

type Event struct {
	TenantID           string
	RuntimeEpoch       string
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

type EventPayload struct {
	Product  *Product
	Dish     *Dish
	Delivery *Delivery
	Batches  []Batch
	Order    *Order
	Pending  *PendingResult
	Writeoff *WriteoffResult
	Expiry   *ExpiryResult
}

type ExpiryResult struct {
	BatchID            string
	ProductID          string
	WrittenOffQuantity Quantity
	Reason             string
	ExpiresAt          time.Time
	ProcessedAt        time.Time
}

type DeliveryReceipt struct {
	OrganizationID, RestaurantID, RuntimeEpoch, LifecycleVersion string
	EvaluatedAt                                                  time.Time
	StateVersion                                                 int64
	Batches                                                      []Batch
	DeliveryID                                                   string
	BatchIDs                                                     []string
}

type CommandResultKind string

const (
	CommandResultProductRegistered      CommandResultKind = "ProductRegistered"
	CommandResultDishDefined            CommandResultKind = "DishDefined"
	CommandResultDeliveryReceived       CommandResultKind = "DeliveryReceived"
	CommandResultOrderPlaced            CommandResultKind = "OrderPlaced"
	CommandResultBatchExpiryPending     CommandResultKind = "BatchExpiryPending"
	CommandResultBatchWriteoffConfirmed CommandResultKind = "BatchWriteoffConfirmed"
	CommandResultBatchExpired           CommandResultKind = "BatchExpired"
)

type CommandResult struct {
	Pending  *PendingResult
	Writeoff *WriteoffResult
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

type CommandBatch struct {
	ExpectedVersion    int64
	CommandID          string
	CommandType        string
	CommandFingerprint string
	Events             []Event
}

type RecordedBatch struct {
	CommandID          string
	CommandType        string
	CommandFingerprint string
	Events             []Event
	FinalSequence      int64
}

type AppendResult struct {
	Batch    RecordedBatch
	Command  CommandRecord
	Replayed bool
}

type Snapshot struct {
	SchemaVersion int64
	StreamID      string
	LastSequence  int64
	State         InventoryState
}

var (
	ErrConcurrentModification  = errors.New("concurrent modification")
	ErrCommandIdentityConflict = errors.New("command identity conflict")
	ErrUnknownWriteOutcome     = errors.New("unknown write outcome")
	ErrInvalidEventBatch       = errors.New("invalid event batch")
	ErrUnsupportedSchema       = errors.New("unsupported event schema")
	ErrProductNotFound         = errors.New("product not found")
	ErrDishNotFound            = errors.New("dish not found")
	ErrBatchNotFound           = errors.New("batch not found")
	ErrInvalidEvent            = errors.New("invalid event")
	ErrInvalidRecordedBatch    = errors.New("invalid recorded batch")
)

func Fingerprint(commandType string, normalizedInput []byte) string {
	input := make([]byte, 0, len(commandType)+1+len(normalizedInput))
	input = append(input, commandType...)
	input = append(input, 0)
	input = append(input, normalizedInput...)
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:])
}

func CanonicalBusinessTime(value time.Time) time.Time {
	return value.Round(0).In(time.FixedZone("MSK", 3*60*60))
}

func CanonicalBusinessTimeString(value time.Time) string {
	return CanonicalBusinessTime(value).Format("2006-01-02T15:04:05.999999999-07:00")
}

type PendingResult struct {
	BatchID           string
	ProductID         string
	RemainingQuantity Quantity
	BaseUnit          BaseUnit
	ExpiresAt         time.Time
}
type WriteoffResult struct {
	BatchID            string
	ProductID          string
	WrittenOffQuantity Quantity
	BaseUnit           BaseUnit
	ActorUserID        string
	ConfirmedAt        time.Time
	EventID            string
	OrganizationID     string
	RestaurantID       string
	RuntimeEpoch       string
	TimeZone           string
	SettingsVersion    string
	StateVersion       int64
}
