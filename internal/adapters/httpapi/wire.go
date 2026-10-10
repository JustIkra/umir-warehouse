package httpapi

import (
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type productRequest struct {
	CommandID *string `json:"commandID"`
	ProductID *string `json:"productID"`
	Name      *string `json:"name"`
	BaseUnit  *string `json:"baseUnit"`
}

type ingredientRequest struct {
	ProductID *string `json:"productID"`
	Quantity  *string `json:"quantity"`
	BaseUnit  *string `json:"baseUnit"`
}

type dishRequest struct {
	CommandID   *string              `json:"commandID"`
	DishID      *string              `json:"dishID"`
	Name        *string              `json:"name"`
	Ingredients *[]ingredientRequest `json:"ingredients"`
}

type positionRequest struct {
	LineNo   *string `json:"lineNo"`
	DishID   *string `json:"dishID"`
	Portions *string `json:"portions"`
}

type orderEvaluationRequest struct {
	Positions *[]positionRequest `json:"positions"`
}

type orderCommitRequest struct {
	CommandID         *string            `json:"commandID"`
	ProposalID        *string            `json:"proposalID"`
	AcceptedPositions *[]positionRequest `json:"acceptedPositions"`
}

type shelfLifeRequest struct {
	Value *string `json:"value"`
	Unit  *string `json:"unit"`
}

type deliveryLineRequest struct {
	ProductID      *string           `json:"productID"`
	ManufacturedAt *string           `json:"manufacturedAt"`
	ShelfLife      *shelfLifeRequest `json:"shelfLife"`
	ReceivedAt     *string           `json:"receivedAt"`
	Quantity       *string           `json:"quantity"`
}

type deliveryRequest struct {
	CommandID  *string                `json:"commandID"`
	DeliveryID *string                `json:"deliveryID"`
	Number     *string                `json:"number"`
	CreatedAt  *string                `json:"createdAt"`
	Lines      *[]deliveryLineRequest `json:"lines"`
}

type errorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Outcome string `json:"outcome"`
}

type productWire struct {
	ProductID string `json:"productID"`
	Name      string `json:"name"`
	BaseUnit  string `json:"baseUnit"`
}

type ingredientWire struct {
	ProductID string `json:"productID"`
	Quantity  string `json:"quantity"`
	BaseUnit  string `json:"baseUnit,omitempty"`
}

type dishWire struct {
	DishID      string           `json:"dishID"`
	Name        string           `json:"name"`
	Ingredients []ingredientWire `json:"ingredients"`
}

type productsWire struct {
	EvaluatedAt  string        `json:"evaluatedAt"`
	StateVersion string        `json:"stateVersion"`
	Items        []productWire `json:"items"`
}

type dishCatalogWire struct {
	EvaluatedAt  string     `json:"evaluatedAt"`
	StateVersion string     `json:"stateVersion"`
	Items        []dishWire `json:"items"`
}

type dishDetailsWire struct {
	EvaluatedAt  string   `json:"evaluatedAt"`
	StateVersion string   `json:"stateVersion"`
	Dish         dishWire `json:"dish"`
}

type deficitWire struct {
	ProductID         string `json:"productID"`
	RequiredQuantity  string `json:"requiredQuantity"`
	AvailableQuantity string `json:"availableQuantity"`
}

type acceptedPositionWire struct {
	LineNo   string `json:"lineNo"`
	DishID   string `json:"dishID"`
	Portions string `json:"portions"`
}

type positionWire struct {
	LineNo   string `json:"lineNo"`
	DishID   string `json:"dishID"`
	Portions string `json:"portions"`
}

type changeWire struct {
	LineNo            string        `json:"lineNo"`
	DishID            string        `json:"dishID"`
	RequestedPortions string        `json:"requestedPortions"`
	AcceptedPortions  string        `json:"acceptedPortions"`
	Deficits          []deficitWire `json:"deficits"`
}

type orderEvaluationWire struct {
	EvaluatedAt        string                 `json:"evaluatedAt"`
	StateVersion       string                 `json:"stateVersion"`
	RequestedPositions []positionWire         `json:"requestedPositions"`
	AcceptedPositions  []acceptedPositionWire `json:"acceptedPositions"`
	Changes            []changeWire           `json:"changes"`
}

type allocationWire struct {
	ProductID string `json:"productID"`
	BatchID   string `json:"batchID"`
	Quantity  string `json:"quantity"`
}

type orderWire struct {
	OrderID           string                 `json:"orderID"`
	ProposalID        string                 `json:"proposalID"`
	AcceptedPositions []acceptedPositionWire `json:"acceptedPositions"`
	Allocations       []allocationWire       `json:"allocations"`
	PlacedAt          string                 `json:"placedAt"`
}

type commitWire struct {
	Kind       contract.CommitResultKind `json:"kind"`
	Order      *orderWire                `json:"order,omitempty"`
	Evaluation *orderEvaluationWire      `json:"evaluation,omitempty"`
}

type menuAllocationWire struct {
	ProductID   string `json:"productID"`
	ProductName string `json:"productName"`
	BatchID     string `json:"batchID"`
	Quantity    string `json:"quantity"`
	BaseUnit    string `json:"baseUnit"`
	ExpiresAt   string `json:"expiresAt"`
}

type menuDishWire struct {
	DishID      string               `json:"dishID"`
	DishName    string               `json:"dishName"`
	IsAvailable bool                 `json:"isAvailable"`
	Deficits    []deficitWire        `json:"deficits"`
	Allocations []menuAllocationWire `json:"allocations"`
}

type menuEvaluationWire struct {
	EvaluatedAt  string         `json:"evaluatedAt"`
	StateVersion string         `json:"stateVersion"`
	Dishes       []menuDishWire `json:"dishes"`
}

type deliveryReceiptWire struct {
	DeliveryID string   `json:"deliveryID"`
	BatchIDs   []string `json:"batchIDs"`
}

type shelfLifeWire struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type deliveryLineWire struct {
	ProductID      string        `json:"productID"`
	ManufacturedAt string        `json:"manufacturedAt"`
	ShelfLife      shelfLifeWire `json:"shelfLife"`
	ReceivedAt     string        `json:"receivedAt"`
	Quantity       string        `json:"quantity"`
}

type deliveryWire struct {
	DeliveryID string             `json:"deliveryID"`
	Number     string             `json:"number"`
	CreatedAt  string             `json:"createdAt"`
	Lines      []deliveryLineWire `json:"lines"`
}

type batchWire struct {
	BatchID           string        `json:"batchID"`
	DeliveryID        string        `json:"deliveryID"`
	LineNo            string        `json:"lineNo"`
	ProductID         string        `json:"productID"`
	ManufacturedAt    string        `json:"manufacturedAt"`
	ShelfLife         shelfLifeWire `json:"shelfLife"`
	ReceivedAt        string        `json:"receivedAt"`
	ExpiresAt         string        `json:"expiresAt"`
	ReceivedQuantity  string        `json:"receivedQuantity"`
	RemainingQuantity string        `json:"remainingQuantity"`
}

type inventoryProductWire struct {
	ProductID string      `json:"productID"`
	BaseUnit  string      `json:"baseUnit"`
	Quantity  string      `json:"quantity"`
	Batches   []batchWire `json:"batches"`
}

type inventoryWire struct {
	EvaluatedAt  string                 `json:"evaluatedAt"`
	StateVersion string                 `json:"stateVersion"`
	Products     []inventoryProductWire `json:"products"`
}

type expiringBatchWire struct {
	BatchID           string `json:"batchID"`
	ProductID         string `json:"productID"`
	RemainingQuantity string `json:"remainingQuantity"`
	BaseUnit          string `json:"baseUnit"`
	ExpiresAt         string `json:"expiresAt"`
	RemainingSeconds  string `json:"remainingSeconds"`
	IsExpired         bool   `json:"isExpired"`
}

type expiringBatchesWire struct {
	EvaluatedAt  string              `json:"evaluatedAt"`
	StateVersion string              `json:"stateVersion"`
	Items        []expiringBatchWire `json:"items"`
}

type expiryWire struct {
	BatchID            string `json:"batchID"`
	ProductID          string `json:"productID"`
	WrittenOffQuantity string `json:"writtenOffQuantity"`
	Reason             string `json:"reason"`
	ExpiresAt          string `json:"expiresAt"`
	ProcessedAt        string `json:"processedAt"`
}

type eventPayloadWire struct {
	Product  *productWire  `json:"product,omitempty"`
	Dish     *dishWire     `json:"dish,omitempty"`
	Delivery *deliveryWire `json:"delivery,omitempty"`
	Batches  []batchWire   `json:"batches,omitempty"`
	Order    *orderWire    `json:"order,omitempty"`
	Expiry   *expiryWire   `json:"expiry,omitempty"`
}

type eventWire struct {
	EventID            string             `json:"eventID"`
	StreamID           string             `json:"streamID"`
	Sequence           string             `json:"sequence"`
	SchemaVersion      int64              `json:"schemaVersion"`
	CommandID          string             `json:"commandID"`
	CommandFingerprint string             `json:"commandFingerprint"`
	RecordedAt         string             `json:"recordedAt"`
	Type               contract.EventType `json:"type"`
	Payload            eventPayloadWire   `json:"payload"`
}

type historyWire struct {
	EvaluatedAt  string      `json:"evaluatedAt"`
	StateVersion string      `json:"stateVersion"`
	Events       []eventWire `json:"events"`
}

type commandResultWire struct {
	Kind     contract.CommandResultKind `json:"kind"`
	Product  *productWire               `json:"product,omitempty"`
	Dish     *dishWire                  `json:"dish,omitempty"`
	Delivery *deliveryReceiptWire       `json:"delivery,omitempty"`
	Order    *orderWire                 `json:"order,omitempty"`
	Expiry   *expiryWire                `json:"expiry,omitempty"`
}

type commandRecordWire struct {
	CommandID          string            `json:"commandID"`
	CommandType        string            `json:"commandType"`
	CommandFingerprint string            `json:"commandFingerprint"`
	Result             commandResultWire `json:"result"`
}

type foundCommandWire struct {
	Found  bool               `json:"found"`
	Record *commandRecordWire `json:"record,omitempty"`
}

type foundOrderWire struct {
	Found bool       `json:"found"`
	Order *orderWire `json:"order,omitempty"`
}

type orderAttemptWire struct {
	State  contract.OrderAttemptState `json:"state"`
	Result *commitWire                `json:"result,omitempty"`
	Error  *errorWire                 `json:"error,omitempty"`
}

func wireTime(value time.Time) string { return value.Format(time.RFC3339Nano) }
func wireInt(value int64) string      { return strconv.FormatInt(value, 10) }

func wireProduct(value contract.Product) productWire {
	return productWire{ProductID: value.ProductID, Name: value.Name, BaseUnit: string(value.BaseUnit)}
}

func wireDish(value contract.Dish, includeBaseUnit bool) dishWire {
	result := dishWire{DishID: value.DishID, Name: value.Name, Ingredients: make([]ingredientWire, len(value.Ingredients))}
	for index, ingredient := range value.Ingredients {
		result.Ingredients[index] = ingredientWire{ProductID: ingredient.ProductID, Quantity: wireInt(int64(ingredient.Quantity))}
		if includeBaseUnit && ingredient.BaseUnit != "" {
			result.Ingredients[index].BaseUnit = string(ingredient.BaseUnit)
		}
	}
	return result
}

func wirePositions(values []contract.Position) []positionWire {
	result := make([]positionWire, len(values))
	for index, value := range values {
		result[index] = positionWire{LineNo: wireInt(value.LineNo), DishID: value.DishID, Portions: wireInt(value.Portions)}
	}
	return result
}

func wireAcceptedPositions(values []contract.AcceptedPosition) []acceptedPositionWire {
	result := make([]acceptedPositionWire, len(values))
	for index, value := range values {
		result[index] = acceptedPositionWire{LineNo: wireInt(value.LineNo), DishID: value.DishID, Portions: wireInt(value.Portions)}
	}
	return result
}

func wireDeficits(values []contract.Deficit) []deficitWire {
	result := make([]deficitWire, len(values))
	for index, value := range values {
		result[index] = deficitWire{ProductID: value.ProductID, RequiredQuantity: wireInt(int64(value.RequiredQuantity)), AvailableQuantity: wireInt(int64(value.AvailableQuantity))}
	}
	return result
}

func wireOrder(value contract.Order) orderWire {
	result := orderWire{OrderID: value.OrderID, ProposalID: value.ProposalID, AcceptedPositions: wireAcceptedPositions(value.AcceptedPositions), Allocations: make([]allocationWire, len(value.Allocations)), PlacedAt: wireTime(value.PlacedAt)}
	for index, allocation := range value.Allocations {
		result.Allocations[index] = allocationWire{ProductID: allocation.ProductID, BatchID: allocation.BatchID, Quantity: wireInt(int64(allocation.Quantity))}
	}
	return result
}

func wireEvaluation(value contract.OrderEvaluation) orderEvaluationWire {
	result := orderEvaluationWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), RequestedPositions: wirePositions(value.RequestedPositions), AcceptedPositions: wireAcceptedPositions(value.AcceptedPositions), Changes: make([]changeWire, len(value.Changes))}
	for index, change := range value.Changes {
		result.Changes[index] = changeWire{LineNo: wireInt(change.LineNo), DishID: change.DishID, RequestedPortions: wireInt(change.RequestedPortions), AcceptedPortions: wireInt(change.AcceptedPortions), Deficits: wireDeficits(change.Deficits)}
	}
	return result
}

func wireCommit(value contract.CommitResult) commitWire {
	result := commitWire{Kind: value.Kind}
	if value.Order != nil {
		order := wireOrder(*value.Order)
		result.Order = &order
	}
	if value.Evaluation != nil {
		evaluation := wireEvaluation(*value.Evaluation)
		result.Evaluation = &evaluation
	}
	return result
}

func wireReceipt(value contract.DeliveryReceipt) deliveryReceiptWire {
	return deliveryReceiptWire{DeliveryID: value.DeliveryID, BatchIDs: append([]string{}, value.BatchIDs...)}
}

func wireShelfLife(value contract.ShelfLife) shelfLifeWire {
	return shelfLifeWire{Value: wireInt(value.Value), Unit: string(value.Unit)}
}

func wireDeliveryLine(value contract.DeliveryLine) deliveryLineWire {
	return deliveryLineWire{ProductID: value.ProductID, ManufacturedAt: wireTime(value.ManufacturedAt), ShelfLife: wireShelfLife(value.ShelfLife), ReceivedAt: wireTime(value.ReceivedAt), Quantity: wireInt(int64(value.Quantity))}
}

func wireDelivery(value contract.Delivery) deliveryWire {
	result := deliveryWire{DeliveryID: value.DeliveryID, Number: value.Number, CreatedAt: wireTime(value.CreatedAt), Lines: make([]deliveryLineWire, len(value.Lines))}
	for index, line := range value.Lines {
		result.Lines[index] = wireDeliveryLine(line)
	}
	return result
}

func wireBatch(value contract.Batch) batchWire {
	return batchWire{BatchID: value.BatchID, DeliveryID: value.DeliveryID, LineNo: wireInt(value.LineNo), ProductID: value.ProductID, ManufacturedAt: wireTime(value.ManufacturedAt), ShelfLife: wireShelfLife(value.ShelfLife), ReceivedAt: wireTime(value.ReceivedAt), ExpiresAt: wireTime(value.ExpiresAt), ReceivedQuantity: wireInt(int64(value.ReceivedQuantity)), RemainingQuantity: wireInt(int64(value.RemainingQuantity))}
}

func wireExpiry(value contract.ExpiryResult) expiryWire {
	return expiryWire{BatchID: value.BatchID, ProductID: value.ProductID, WrittenOffQuantity: wireInt(int64(value.WrittenOffQuantity)), Reason: value.Reason, ExpiresAt: wireTime(value.ExpiresAt), ProcessedAt: wireTime(value.ProcessedAt)}
}

func wireProducts(value contract.Products) productsWire {
	result := productsWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Items: make([]productWire, len(value.Items))}
	for index, product := range value.Items {
		result.Items[index] = wireProduct(product)
	}
	return result
}

func wireDishCatalog(value contract.DishCatalog) dishCatalogWire {
	result := dishCatalogWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Items: make([]dishWire, len(value.Items))}
	for index, dish := range value.Items {
		result.Items[index] = wireDish(dish, true)
	}
	return result
}

func wireDishDetails(value contract.DishDetails) dishDetailsWire {
	return dishDetailsWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Dish: wireDish(value.Dish, true)}
}

func wireMenuEvaluation(value contract.MenuEvaluation) menuEvaluationWire {
	result := menuEvaluationWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Dishes: make([]menuDishWire, len(value.Dishes))}
	for index, dish := range value.Dishes {
		item := menuDishWire{DishID: dish.DishID, DishName: dish.DishName, IsAvailable: dish.IsAvailable, Deficits: wireDeficits(dish.Deficits), Allocations: make([]menuAllocationWire, len(dish.Allocations))}
		for allocationIndex, allocation := range dish.Allocations {
			item.Allocations[allocationIndex] = menuAllocationWire{ProductID: allocation.ProductID, ProductName: allocation.ProductName, BatchID: allocation.BatchID, Quantity: wireInt(int64(allocation.Quantity)), BaseUnit: string(allocation.BaseUnit), ExpiresAt: wireTime(allocation.ExpiresAt)}
		}
		result.Dishes[index] = item
	}
	return result
}

func wireInventory(value contract.InventoryView) inventoryWire {
	result := inventoryWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Products: make([]inventoryProductWire, len(value.Products))}
	for index, product := range value.Products {
		item := inventoryProductWire{ProductID: product.ProductID, BaseUnit: string(product.BaseUnit), Quantity: wireInt(int64(product.Quantity)), Batches: make([]batchWire, len(product.Batches))}
		for batchIndex, batch := range product.Batches {
			item.Batches[batchIndex] = wireBatch(batch)
		}
		result.Products[index] = item
	}
	return result
}

func wireExpiringBatches(value contract.ExpiringBatches) expiringBatchesWire {
	result := expiringBatchesWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Items: make([]expiringBatchWire, len(value.Items))}
	for index, batch := range value.Items {
		result.Items[index] = expiringBatchWire{BatchID: batch.BatchID, ProductID: batch.ProductID, RemainingQuantity: wireInt(int64(batch.RemainingQuantity)), BaseUnit: string(batch.BaseUnit), ExpiresAt: wireTime(batch.ExpiresAt), RemainingSeconds: wireInt(batch.RemainingSeconds), IsExpired: batch.IsExpired}
	}
	return result
}

func wireHistory(value contract.History) historyWire {
	result := historyWire{EvaluatedAt: wireTime(value.EvaluatedAt), StateVersion: wireInt(value.StateVersion), Events: make([]eventWire, len(value.Events))}
	for index, event := range value.Events {
		item := eventWire{EventID: event.EventID, StreamID: event.StreamID, Sequence: wireInt(event.Sequence), SchemaVersion: event.SchemaVersion, CommandID: event.CommandID, CommandFingerprint: event.CommandFingerprint, RecordedAt: wireTime(event.RecordedAt), Type: event.Type}
		if event.Payload.Product != nil {
			product := wireProduct(*event.Payload.Product)
			item.Payload.Product = &product
		}
		if event.Payload.Dish != nil {
			dish := wireDish(*event.Payload.Dish, false)
			item.Payload.Dish = &dish
		}
		if event.Payload.Delivery != nil {
			delivery := wireDelivery(*event.Payload.Delivery)
			item.Payload.Delivery = &delivery
		}
		if event.Payload.Batches != nil {
			item.Payload.Batches = make([]batchWire, len(event.Payload.Batches))
			for batchIndex, batch := range event.Payload.Batches {
				item.Payload.Batches[batchIndex] = wireBatch(batch)
			}
		}
		if event.Payload.Order != nil {
			order := wireOrder(*event.Payload.Order)
			item.Payload.Order = &order
		}
		if event.Payload.Expiry != nil {
			expiry := wireExpiry(*event.Payload.Expiry)
			item.Payload.Expiry = &expiry
		}
		result.Events[index] = item
	}
	return result
}

func wireCommandRecord(value contract.CommandRecord) commandRecordWire {
	result := commandRecordWire{CommandID: value.CommandID, CommandType: value.CommandType, CommandFingerprint: value.CommandFingerprint, Result: commandResultWire{Kind: value.Result.Kind}}
	if value.Result.Product != nil {
		product := wireProduct(*value.Result.Product)
		result.Result.Product = &product
	}
	if value.Result.Dish != nil {
		dish := wireDish(*value.Result.Dish, false)
		result.Result.Dish = &dish
	}
	if value.Result.Delivery != nil {
		delivery := wireReceipt(*value.Result.Delivery)
		result.Result.Delivery = &delivery
	}
	if value.Result.Order != nil {
		order := wireOrder(*value.Result.Order)
		result.Result.Order = &order
	}
	if value.Result.Expiry != nil {
		expiry := wireExpiry(*value.Result.Expiry)
		result.Result.Expiry = &expiry
	}
	return result
}
