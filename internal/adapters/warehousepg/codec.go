package warehousepg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type eventDTO struct {
	TenantID           string           `json:"tenantID"`
	RuntimeEpoch       string           `json:"runtimeEpoch"`
	EventID            string           `json:"eventID"`
	StreamID           string           `json:"streamID"`
	Sequence           int64            `json:"sequence"`
	SchemaVersion      int64            `json:"schemaVersion"`
	CommandID          string           `json:"commandID"`
	CommandFingerprint string           `json:"commandFingerprint"`
	RecordedAt         string           `json:"recordedAt"`
	Type               domain.EventType `json:"type"`
	Payload            payloadDTO       `json:"payload"`
}

type payloadDTO struct {
	Product  *productDTO  `json:"product,omitempty"`
	Dish     *dishDTO     `json:"dish,omitempty"`
	Delivery *deliveryDTO `json:"delivery,omitempty"`
	Batches  []batchDTO   `json:"batches,omitempty"`
	Order    *orderDTO    `json:"order,omitempty"`
	Pending  *pendingDTO  `json:"pending,omitempty"`
	Writeoff *writeoffDTO `json:"writeoff,omitempty"`
	Expiry   *expiryDTO   `json:"expiry,omitempty"`
}

type productDTO struct {
	ProductID string          `json:"productID"`
	Name      string          `json:"name"`
	BaseUnit  domain.BaseUnit `json:"baseUnit"`
}
type ingredientDTO struct {
	ProductID string `json:"productID"`
	Quantity  string `json:"quantity"`
}
type dishDTO struct {
	DishID      string          `json:"dishID"`
	Name        string          `json:"name"`
	Ingredients []ingredientDTO `json:"ingredients"`
}
type shelfLifeDTO struct {
	Value int64                `json:"value"`
	Unit  domain.ShelfLifeUnit `json:"unit"`
}
type deliveryLineDTO struct {
	ProductID      string       `json:"productID"`
	ManufacturedAt string       `json:"manufacturedAt"`
	ShelfLife      shelfLifeDTO `json:"shelfLife"`
	ReceivedAt     string       `json:"receivedAt"`
	Quantity       string       `json:"quantity"`
}
type deliveryDTO struct {
	OrganizationID   string            `json:"organizationID"`
	LifecycleVersion string            `json:"lifecycleVersion"`
	DeliveryID       string            `json:"deliveryID"`
	Number           string            `json:"number"`
	CreatedAt        string            `json:"createdAt"`
	Lines            []deliveryLineDTO `json:"lines"`
}
type batchDTO struct {
	AppliedTimeZone        string       `json:"appliedTimeZone"`
	AppliedSettingsVersion string       `json:"appliedSettingsVersion"`
	TZDataVersion          string       `json:"tzDataVersion"`
	BatchID                string       `json:"batchID"`
	DeliveryID             string       `json:"deliveryID"`
	LineNo                 int64        `json:"lineNo"`
	ProductID              string       `json:"productID"`
	ManufacturedAt         string       `json:"manufacturedAt"`
	ShelfLife              shelfLifeDTO `json:"shelfLife"`
	ReceivedAt             string       `json:"receivedAt"`
	ExpiresAt              string       `json:"expiresAt"`
	ReceivedQuantity       string       `json:"receivedQuantity"`
	RemainingQuantity      string       `json:"remainingQuantity"`
	ConsumedByOrders       string       `json:"consumedByOrders"`
	PendingWriteoff        bool         `json:"pendingWriteoff"`
	WrittenOffForExpiry    string       `json:"writtenOffForExpiry"`
}
type acceptedPositionDTO struct {
	LineNo   int64  `json:"lineNo"`
	DishID   string `json:"dishID"`
	Portions int64  `json:"portions"`
}
type allocationDTO struct {
	ProductID string `json:"productID"`
	BatchID   string `json:"batchID"`
	Quantity  string `json:"quantity"`
}
type orderDTO struct {
	OrderID           string                `json:"orderID"`
	ProposalID        string                `json:"proposalID"`
	AcceptedPositions []acceptedPositionDTO `json:"acceptedPositions"`
	Allocations       []allocationDTO       `json:"allocations"`
	PlacedAt          string                `json:"placedAt"`
}
type pendingDTO struct {
	BatchID           string          `json:"batchID"`
	ProductID         string          `json:"productID"`
	RemainingQuantity string          `json:"remainingQuantity"`
	BaseUnit          domain.BaseUnit `json:"baseUnit"`
	ExpiresAt         string          `json:"expiresAt"`
}
type writeoffDTO struct {
	BatchID            string          `json:"batchID"`
	ProductID          string          `json:"productID"`
	WrittenOffQuantity string          `json:"writtenOffQuantity"`
	BaseUnit           domain.BaseUnit `json:"baseUnit"`
	ActorUserID        string          `json:"actorUserID"`
	ConfirmedAt        string          `json:"confirmedAt"`
	EventID            string          `json:"eventID"`
	OrganizationID     string          `json:"organizationID"`
	RestaurantID       string          `json:"restaurantID"`
	RuntimeEpoch       string          `json:"runtimeEpoch"`
	TimeZone           string          `json:"timeZone"`
	SettingsVersion    string          `json:"settingsVersion"`
	StateVersion       int64           `json:"stateVersion"`
}
type expiryDTO struct {
	BatchID            string `json:"batchID"`
	ProductID          string `json:"productID"`
	WrittenOffQuantity string `json:"writtenOffQuantity"`
	Reason             string `json:"reason"`
	ExpiresAt          string `json:"expiresAt"`
	ProcessedAt        string `json:"processedAt"`
}

type commandResultDTO struct {
	Kind     domain.CommandResultKind `json:"kind"`
	Product  *productDTO              `json:"product,omitempty"`
	Dish     *dishDTO                 `json:"dish,omitempty"`
	Delivery *deliveryReceiptDTO      `json:"delivery,omitempty"`
	Order    *orderDTO                `json:"order,omitempty"`
	Pending  *pendingDTO              `json:"pending,omitempty"`
	Writeoff *writeoffDTO             `json:"writeoff,omitempty"`
	Expiry   *expiryDTO               `json:"expiry,omitempty"`
}
type deliveryReceiptDTO struct {
	OrganizationID   string     `json:"organizationID"`
	RestaurantID     string     `json:"restaurantID"`
	RuntimeEpoch     string     `json:"runtimeEpoch"`
	LifecycleVersion string     `json:"lifecycleVersion"`
	EvaluatedAt      string     `json:"evaluatedAt"`
	StateVersion     int64      `json:"stateVersion"`
	Batches          []batchDTO `json:"batches"`
	DeliveryID       string     `json:"deliveryID"`
	BatchIDs         []string   `json:"batchIDs"`
}

type stateDTO struct {
	Products     map[string]productDTO `json:"products"`
	Dishes       map[string]dishDTO    `json:"dishes"`
	DeliveryIDs  []string              `json:"deliveryIDs"`
	Batches      map[string]batchDTO   `json:"batches"`
	StateVersion int64                 `json:"stateVersion"`
}

func encodeEvent(event domain.Event) ([]byte, error) {
	dto, err := toEventDTO(event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(dto)
}

func decodeEvent(data []byte) (domain.Event, error) {
	var dto eventDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return domain.Event{}, err
	}
	return fromEventDTO(dto)
}

func toEventDTO(event domain.Event) (eventDTO, error) {
	dto := eventDTO{
		TenantID: event.TenantID, RuntimeEpoch: event.RuntimeEpoch, EventID: event.EventID,
		StreamID: event.StreamID, Sequence: event.Sequence, SchemaVersion: event.SchemaVersion,
		CommandID: event.CommandID, CommandFingerprint: event.CommandFingerprint,
		RecordedAt: businessTime(event.RecordedAt), Type: event.Type,
	}
	var err error
	if p := event.Payload.Product; p != nil {
		v := productDTO{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
		dto.Payload.Product = &v
	}
	if dish := event.Payload.Dish; dish != nil {
		v, e := toDishDTO(*dish)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Dish = &v
	}
	if delivery := event.Payload.Delivery; delivery != nil {
		v, e := toDeliveryDTO(*delivery)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Delivery = &v
	}
	for _, batch := range event.Payload.Batches {
		v, e := toBatchDTO(batch)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Batches = append(dto.Payload.Batches, v)
	}
	if order := event.Payload.Order; order != nil {
		v, e := toOrderDTO(*order)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Order = &v
	}
	if pending := event.Payload.Pending; pending != nil {
		v, e := toPendingDTO(*pending)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Pending = &v
	}
	if writeoff := event.Payload.Writeoff; writeoff != nil {
		v, e := toWriteoffDTO(*writeoff)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Writeoff = &v
	}
	if expiry := event.Payload.Expiry; expiry != nil {
		v, e := toExpiryDTO(*expiry)
		if e != nil {
			return eventDTO{}, e
		}
		dto.Payload.Expiry = &v
	}
	return dto, err
}

func fromEventDTO(dto eventDTO) (domain.Event, error) {
	recordedAt, err := parseBusinessTime(dto.RecordedAt)
	if err != nil {
		return domain.Event{}, err
	}
	event := domain.Event{TenantID: dto.TenantID, RuntimeEpoch: dto.RuntimeEpoch, EventID: dto.EventID,
		StreamID: dto.StreamID, Sequence: dto.Sequence, SchemaVersion: dto.SchemaVersion,
		CommandID: dto.CommandID, CommandFingerprint: dto.CommandFingerprint, RecordedAt: recordedAt, Type: dto.Type}
	if p := dto.Payload.Product; p != nil {
		v := domain.Product{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
		event.Payload.Product = &v
	}
	if d := dto.Payload.Dish; d != nil {
		v, e := fromDishDTO(*d)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Dish = &v
	}
	if d := dto.Payload.Delivery; d != nil {
		v, e := fromDeliveryDTO(*d)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Delivery = &v
	}
	for _, b := range dto.Payload.Batches {
		v, e := fromBatchDTO(b)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Batches = append(event.Payload.Batches, v)
	}
	if o := dto.Payload.Order; o != nil {
		v, e := fromOrderDTO(*o)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Order = &v
	}
	if p := dto.Payload.Pending; p != nil {
		v, e := fromPendingDTO(*p)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Pending = &v
	}
	if w := dto.Payload.Writeoff; w != nil {
		v, e := fromWriteoffDTO(*w)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Writeoff = &v
	}
	if x := dto.Payload.Expiry; x != nil {
		v, e := fromExpiryDTO(*x)
		if e != nil {
			return domain.Event{}, e
		}
		event.Payload.Expiry = &v
	}
	return event, nil
}

func encodeResult(result domain.CommandResult) ([]byte, error) {
	dto := commandResultDTO{Kind: result.Kind}
	if p := result.Product; p != nil {
		v := productDTO{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
		dto.Product = &v
	}
	if d := result.Dish; d != nil {
		v, err := toDishDTO(*d)
		if err != nil {
			return nil, err
		}
		dto.Dish = &v
	}
	if d := result.Delivery; d != nil {
		v := deliveryReceiptDTO{OrganizationID: d.OrganizationID, RestaurantID: d.RestaurantID, RuntimeEpoch: d.RuntimeEpoch,
			LifecycleVersion: d.LifecycleVersion, EvaluatedAt: businessTime(d.EvaluatedAt), StateVersion: d.StateVersion,
			DeliveryID: d.DeliveryID, BatchIDs: append([]string(nil), d.BatchIDs...)}
		for _, b := range d.Batches {
			encoded, err := toBatchDTO(b)
			if err != nil {
				return nil, err
			}
			v.Batches = append(v.Batches, encoded)
		}
		dto.Delivery = &v
	}
	if o := result.Order; o != nil {
		v, err := toOrderDTO(*o)
		if err != nil {
			return nil, err
		}
		dto.Order = &v
	}
	if p := result.Pending; p != nil {
		v, err := toPendingDTO(*p)
		if err != nil {
			return nil, err
		}
		dto.Pending = &v
	}
	if w := result.Writeoff; w != nil {
		v, err := toWriteoffDTO(*w)
		if err != nil {
			return nil, err
		}
		dto.Writeoff = &v
	}
	if x := result.Expiry; x != nil {
		v, err := toExpiryDTO(*x)
		if err != nil {
			return nil, err
		}
		dto.Expiry = &v
	}
	return json.Marshal(dto)
}

func decodeResult(data []byte) (domain.CommandResult, error) {
	var dto commandResultDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return domain.CommandResult{}, err
	}
	if dto.Kind == "" {
		return domain.CommandResult{}, errors.New("stored command result has no kind")
	}
	result := domain.CommandResult{Kind: dto.Kind}
	if p := dto.Product; p != nil {
		v := domain.Product{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
		result.Product = &v
	}
	if d := dto.Dish; d != nil {
		v, err := fromDishDTO(*d)
		if err != nil {
			return domain.CommandResult{}, err
		}
		result.Dish = &v
	}
	if d := dto.Delivery; d != nil {
		evaluatedAt, err := parseBusinessTime(d.EvaluatedAt)
		if err != nil {
			return domain.CommandResult{}, err
		}
		v := domain.DeliveryReceipt{OrganizationID: d.OrganizationID, RestaurantID: d.RestaurantID, RuntimeEpoch: d.RuntimeEpoch,
			LifecycleVersion: d.LifecycleVersion, EvaluatedAt: evaluatedAt, StateVersion: d.StateVersion,
			DeliveryID: d.DeliveryID, BatchIDs: append([]string(nil), d.BatchIDs...)}
		for _, b := range d.Batches {
			decoded, err := fromBatchDTO(b)
			if err != nil {
				return domain.CommandResult{}, err
			}
			v.Batches = append(v.Batches, decoded)
		}
		result.Delivery = &v
	}
	if o := dto.Order; o != nil {
		v, err := fromOrderDTO(*o)
		if err != nil {
			return domain.CommandResult{}, err
		}
		result.Order = &v
	}
	if p := dto.Pending; p != nil {
		v, err := fromPendingDTO(*p)
		if err != nil {
			return domain.CommandResult{}, err
		}
		result.Pending = &v
	}
	if w := dto.Writeoff; w != nil {
		v, err := fromWriteoffDTO(*w)
		if err != nil {
			return domain.CommandResult{}, err
		}
		result.Writeoff = &v
	}
	if x := dto.Expiry; x != nil {
		v, err := fromExpiryDTO(*x)
		if err != nil {
			return domain.CommandResult{}, err
		}
		result.Expiry = &v
	}
	return result, nil
}

func encodeSnapshotState(state domain.InventoryState) ([]byte, string, error) {
	dto := stateDTO{Products: make(map[string]productDTO, len(state.Products)), Dishes: make(map[string]dishDTO, len(state.Dishes)),
		Batches: make(map[string]batchDTO, len(state.Batches)), StateVersion: state.StateVersion}
	for id, p := range state.Products {
		dto.Products[id] = productDTO{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
	}
	for id, d := range state.Dishes {
		encoded, err := toDishDTO(d)
		if err != nil {
			return nil, "", err
		}
		dto.Dishes[id] = encoded
	}
	for id := range state.DeliveryIDs {
		dto.DeliveryIDs = append(dto.DeliveryIDs, id)
	}
	sort.Strings(dto.DeliveryIDs)
	for id, b := range state.Batches {
		encoded, err := toBatchDTO(b)
		if err != nil {
			return nil, "", err
		}
		dto.Batches[id] = encoded
	}
	data, err := json.Marshal(dto)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

func decodeSnapshotState(data []byte, expectedHash string) (domain.InventoryState, error) {
	var dto stateDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return domain.InventoryState{}, err
	}
	state := domain.InventoryState{Products: make(map[string]domain.Product, len(dto.Products)), Dishes: make(map[string]domain.Dish, len(dto.Dishes)),
		DeliveryIDs: make(map[string]struct{}, len(dto.DeliveryIDs)), Batches: make(map[string]domain.Batch, len(dto.Batches)), StateVersion: dto.StateVersion}
	for id, p := range dto.Products {
		state.Products[id] = domain.Product{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit}
	}
	for id, d := range dto.Dishes {
		decoded, err := fromDishDTO(d)
		if err != nil {
			return domain.InventoryState{}, err
		}
		state.Dishes[id] = decoded
	}
	for _, id := range dto.DeliveryIDs {
		state.DeliveryIDs[id] = struct{}{}
	}
	for id, b := range dto.Batches {
		decoded, err := fromBatchDTO(b)
		if err != nil {
			return domain.InventoryState{}, err
		}
		state.Batches[id] = decoded
	}
	canonical, hash, err := encodeSnapshotState(state)
	if err != nil {
		return domain.InventoryState{}, err
	}
	if hash != expectedHash || !json.Valid(canonical) {
		return domain.InventoryState{}, errors.New("stored snapshot checksum mismatch")
	}
	state.Reindex()
	return state, nil
}

func toDishDTO(d domain.Dish) (dishDTO, error) {
	result := dishDTO{DishID: d.DishID, Name: d.Name}
	for _, ingredient := range d.Ingredients {
		result.Ingredients = append(result.Ingredients, ingredientDTO{ProductID: ingredient.ProductID, Quantity: strconv.FormatInt(int64(ingredient.Quantity), 10)})
	}
	return result, nil
}
func fromDishDTO(d dishDTO) (domain.Dish, error) {
	result := domain.Dish{DishID: d.DishID, Name: d.Name}
	for _, ingredient := range d.Ingredients {
		q, err := quantity(ingredient.Quantity)
		if err != nil {
			return domain.Dish{}, err
		}
		result.Ingredients = append(result.Ingredients, domain.Ingredient{ProductID: ingredient.ProductID, Quantity: q})
	}
	return result, nil
}
func toDeliveryDTO(d domain.Delivery) (deliveryDTO, error) {
	result := deliveryDTO{OrganizationID: d.OrganizationID, LifecycleVersion: d.LifecycleVersion, DeliveryID: d.DeliveryID, Number: d.Number, CreatedAt: businessTime(d.CreatedAt)}
	for _, line := range d.Lines {
		result.Lines = append(result.Lines, deliveryLineDTO{ProductID: line.ProductID, ManufacturedAt: businessTime(line.ManufacturedAt), ShelfLife: shelfLifeDTO{Value: line.ShelfLife.Value, Unit: line.ShelfLife.Unit}, ReceivedAt: businessTime(line.ReceivedAt), Quantity: strconv.FormatInt(int64(line.Quantity), 10)})
	}
	return result, nil
}
func fromDeliveryDTO(d deliveryDTO) (domain.Delivery, error) {
	created, err := parseBusinessTime(d.CreatedAt)
	if err != nil {
		return domain.Delivery{}, err
	}
	result := domain.Delivery{OrganizationID: d.OrganizationID, LifecycleVersion: d.LifecycleVersion, DeliveryID: d.DeliveryID, Number: d.Number, CreatedAt: created}
	for _, line := range d.Lines {
		manufactured, err := parseBusinessTime(line.ManufacturedAt)
		if err != nil {
			return domain.Delivery{}, err
		}
		received, err := parseBusinessTime(line.ReceivedAt)
		if err != nil {
			return domain.Delivery{}, err
		}
		q, err := quantity(line.Quantity)
		if err != nil {
			return domain.Delivery{}, err
		}
		result.Lines = append(result.Lines, domain.DeliveryLine{ProductID: line.ProductID, ManufacturedAt: manufactured, ShelfLife: domain.ShelfLife{Value: line.ShelfLife.Value, Unit: line.ShelfLife.Unit}, ReceivedAt: received, Quantity: q})
	}
	return result, nil
}
func toBatchDTO(b domain.Batch) (batchDTO, error) {
	return batchDTO{AppliedTimeZone: b.AppliedTimeZone, AppliedSettingsVersion: b.AppliedSettingsVersion, TZDataVersion: b.TZDataVersion,
		BatchID: b.BatchID, DeliveryID: b.DeliveryID, LineNo: b.LineNo, ProductID: b.ProductID,
		ManufacturedAt: businessTime(b.ManufacturedAt), ShelfLife: shelfLifeDTO{Value: b.ShelfLife.Value, Unit: b.ShelfLife.Unit},
		ReceivedAt: businessTime(b.ReceivedAt), ExpiresAt: businessTime(b.ExpiresAt),
		ReceivedQuantity: strconv.FormatInt(int64(b.ReceivedQuantity), 10), RemainingQuantity: strconv.FormatInt(int64(b.RemainingQuantity), 10),
		ConsumedByOrders: strconv.FormatInt(int64(b.ConsumedByOrders), 10), PendingWriteoff: b.PendingWriteoff,
		WrittenOffForExpiry: strconv.FormatInt(int64(b.WrittenOffForExpiry), 10)}, nil
}
func fromBatchDTO(b batchDTO) (domain.Batch, error) {
	manufactured, err := parseBusinessTime(b.ManufacturedAt)
	if err != nil {
		return domain.Batch{}, err
	}
	received, err := parseBusinessTime(b.ReceivedAt)
	if err != nil {
		return domain.Batch{}, err
	}
	expires, err := parseBusinessTime(b.ExpiresAt)
	if err != nil {
		return domain.Batch{}, err
	}
	receivedQ, err := quantity(b.ReceivedQuantity)
	if err != nil {
		return domain.Batch{}, err
	}
	remainingQ, err := quantity(b.RemainingQuantity)
	if err != nil {
		return domain.Batch{}, err
	}
	consumedQ, err := quantity(b.ConsumedByOrders)
	if err != nil {
		return domain.Batch{}, err
	}
	writtenOffQ, err := quantity(b.WrittenOffForExpiry)
	if err != nil {
		return domain.Batch{}, err
	}
	return domain.Batch{AppliedTimeZone: b.AppliedTimeZone, AppliedSettingsVersion: b.AppliedSettingsVersion, TZDataVersion: b.TZDataVersion,
		BatchID: b.BatchID, DeliveryID: b.DeliveryID, LineNo: b.LineNo, ProductID: b.ProductID,
		ManufacturedAt: manufactured, ShelfLife: domain.ShelfLife{Value: b.ShelfLife.Value, Unit: b.ShelfLife.Unit}, ReceivedAt: received, ExpiresAt: expires,
		ReceivedQuantity: receivedQ, RemainingQuantity: remainingQ, ConsumedByOrders: consumedQ, PendingWriteoff: b.PendingWriteoff, WrittenOffForExpiry: writtenOffQ}, nil
}
func toOrderDTO(o domain.Order) (orderDTO, error) {
	result := orderDTO{OrderID: o.OrderID, ProposalID: o.ProposalID, PlacedAt: businessTime(o.PlacedAt)}
	for _, p := range o.AcceptedPositions {
		result.AcceptedPositions = append(result.AcceptedPositions, acceptedPositionDTO{LineNo: p.LineNo, DishID: p.DishID, Portions: p.Portions})
	}
	for _, a := range o.Allocations {
		result.Allocations = append(result.Allocations, allocationDTO{ProductID: a.ProductID, BatchID: a.BatchID, Quantity: strconv.FormatInt(int64(a.Quantity), 10)})
	}
	return result, nil
}
func fromOrderDTO(o orderDTO) (domain.Order, error) {
	placed, err := parseBusinessTime(o.PlacedAt)
	if err != nil {
		return domain.Order{}, err
	}
	result := domain.Order{OrderID: o.OrderID, ProposalID: o.ProposalID, PlacedAt: placed}
	for _, p := range o.AcceptedPositions {
		result.AcceptedPositions = append(result.AcceptedPositions, domain.AcceptedPosition{LineNo: p.LineNo, DishID: p.DishID, Portions: p.Portions})
	}
	for _, a := range o.Allocations {
		q, err := quantity(a.Quantity)
		if err != nil {
			return domain.Order{}, err
		}
		result.Allocations = append(result.Allocations, domain.Allocation{ProductID: a.ProductID, BatchID: a.BatchID, Quantity: q})
	}
	return result, nil
}
func toPendingDTO(p domain.PendingResult) (pendingDTO, error) {
	return pendingDTO{BatchID: p.BatchID, ProductID: p.ProductID, RemainingQuantity: strconv.FormatInt(int64(p.RemainingQuantity), 10), BaseUnit: p.BaseUnit, ExpiresAt: businessTime(p.ExpiresAt)}, nil
}
func fromPendingDTO(p pendingDTO) (domain.PendingResult, error) {
	q, err := quantity(p.RemainingQuantity)
	if err != nil {
		return domain.PendingResult{}, err
	}
	expires, err := parseBusinessTime(p.ExpiresAt)
	if err != nil {
		return domain.PendingResult{}, err
	}
	return domain.PendingResult{BatchID: p.BatchID, ProductID: p.ProductID, RemainingQuantity: q, BaseUnit: p.BaseUnit, ExpiresAt: expires}, nil
}
func toWriteoffDTO(w domain.WriteoffResult) (writeoffDTO, error) {
	return writeoffDTO{BatchID: w.BatchID, ProductID: w.ProductID, WrittenOffQuantity: strconv.FormatInt(int64(w.WrittenOffQuantity), 10), BaseUnit: w.BaseUnit, ActorUserID: w.ActorUserID, ConfirmedAt: businessTime(w.ConfirmedAt), EventID: w.EventID, OrganizationID: w.OrganizationID, RestaurantID: w.RestaurantID, RuntimeEpoch: w.RuntimeEpoch, TimeZone: w.TimeZone, SettingsVersion: w.SettingsVersion, StateVersion: w.StateVersion}, nil
}
func fromWriteoffDTO(w writeoffDTO) (domain.WriteoffResult, error) {
	q, err := quantity(w.WrittenOffQuantity)
	if err != nil {
		return domain.WriteoffResult{}, err
	}
	confirmed, err := parseBusinessTime(w.ConfirmedAt)
	if err != nil {
		return domain.WriteoffResult{}, err
	}
	return domain.WriteoffResult{BatchID: w.BatchID, ProductID: w.ProductID, WrittenOffQuantity: q, BaseUnit: w.BaseUnit, ActorUserID: w.ActorUserID, ConfirmedAt: confirmed, EventID: w.EventID, OrganizationID: w.OrganizationID, RestaurantID: w.RestaurantID, RuntimeEpoch: w.RuntimeEpoch, TimeZone: w.TimeZone, SettingsVersion: w.SettingsVersion, StateVersion: w.StateVersion}, nil
}
func toExpiryDTO(x domain.ExpiryResult) (expiryDTO, error) {
	return expiryDTO{BatchID: x.BatchID, ProductID: x.ProductID, WrittenOffQuantity: strconv.FormatInt(int64(x.WrittenOffQuantity), 10), Reason: x.Reason, ExpiresAt: businessTime(x.ExpiresAt), ProcessedAt: businessTime(x.ProcessedAt)}, nil
}
func fromExpiryDTO(x expiryDTO) (domain.ExpiryResult, error) {
	q, err := quantity(x.WrittenOffQuantity)
	if err != nil {
		return domain.ExpiryResult{}, err
	}
	expires, err := parseBusinessTime(x.ExpiresAt)
	if err != nil {
		return domain.ExpiryResult{}, err
	}
	processed, err := parseBusinessTime(x.ProcessedAt)
	if err != nil {
		return domain.ExpiryResult{}, err
	}
	return domain.ExpiryResult{BatchID: x.BatchID, ProductID: x.ProductID, WrittenOffQuantity: q, Reason: x.Reason, ExpiresAt: expires, ProcessedAt: processed}, nil
}

func businessTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return domain.CanonicalBusinessTimeString(value)
}
func parseBusinessTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode business time: %w", err)
	}
	return parsed, nil
}
func quantity(value string) (domain.Quantity, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode quantity: %w", err)
	}
	return domain.Quantity(parsed), nil
}
