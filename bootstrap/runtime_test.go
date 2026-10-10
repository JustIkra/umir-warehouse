package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

type versionFailureStore struct {
	*memory.EventStore
	failVersion bool
}

func (s *versionFailureStore) Version(ctx context.Context) (int64, error) {
	if s.failVersion {
		return 0, errors.New("injected projection lookup failure")
	}
	return s.EventStore.Version(ctx)
}

func TestPublicRuntimeOptions(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	clockCalls := 0
	idCalls := 0
	options := Options{
		Now: func() time.Time {
			clockCalls++
			return now
		},
		NewID: func() string {
			idCalls++
			return fmt.Sprint("id-", idCalls)
		},
		EventsPerSnapshot: 10,
	}

	invalid := []struct {
		name    string
		options Options
	}{
		{name: "nil clock", options: Options{NewID: options.NewID, EventsPerSnapshot: 10}},
		{name: "nil id generator", options: Options{Now: options.Now, EventsPerSnapshot: 10}},
		{name: "zero snapshot threshold", options: Options{Now: options.Now, NewID: options.NewID}},
		{name: "negative snapshot threshold", options: Options{Now: options.Now, NewID: options.NewID, EventsPerSnapshot: -1}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewInMemory(test.options); !errors.Is(err, contract.ErrInvalidArgument) {
				t.Fatalf("NewInMemory() error = %v, want ErrInvalidArgument", err)
			}
			if clockCalls != 0 || idCalls != 0 {
				t.Fatalf("invalid constructor called callbacks: now=%d id=%d", clockCalls, idCalls)
			}
		})
	}

	runtime, err := NewInMemory(options)
	if err != nil {
		t.Fatalf("NewInMemory() error = %v", err)
	}
	if clockCalls != 0 || idCalls != 0 {
		t.Fatalf("constructor called callbacks: now=%d id=%d", clockCalls, idCalls)
	}

	products, err := runtime.GetProducts(context.Background())
	if err != nil {
		t.Fatalf("GetProducts() error = %v", err)
	}
	if len(products.Items) != 0 || products.StateVersion != 0 {
		t.Fatalf("empty products = %+v", products)
	}
	dishes, err := runtime.GetDishCatalog(context.Background())
	if err != nil || len(dishes.Items) != 0 || dishes.StateVersion != 0 {
		t.Fatalf("empty dishes = %+v, %v", dishes, err)
	}
	inventory, err := runtime.GetInventory(context.Background())
	if err != nil || len(inventory.Products) != 0 || inventory.StateVersion != 0 {
		t.Fatalf("empty inventory = %+v, %v", inventory, err)
	}
	history, err := runtime.GetHistory(context.Background())
	if err != nil || len(history.Events) != 0 || history.StateVersion != 0 {
		t.Fatalf("empty history = %+v, %v", history, err)
	}
	menu, err := runtime.EvaluateMenu(context.Background())
	if err != nil || len(menu.Dishes) != 0 || menu.StateVersion != 0 {
		t.Fatalf("empty menu = %+v, %v", menu, err)
	}
	if clockCalls == 0 || idCalls != 0 {
		t.Fatalf("empty read callbacks: now=%d id=%d", clockCalls, idCalls)
	}
}

func TestRuntimeDelegatesWarehouseOperations(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	nextID := 0
	runtime, err := NewInMemory(Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			nextID++
			return fmt.Sprintf("generated-%d", nextID)
		},
		EventsPerSnapshot: 1,
	})
	if err != nil {
		t.Fatalf("NewInMemory() error = %v", err)
	}
	if err := runtime.Recover(ctx); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}

	product, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "command-product", ProductID: "milk", Name: "Milk", BaseUnit: contract.BaseUnitML})
	if err != nil || product.ProductID != "milk" {
		t.Fatalf("RegisterProduct() = %+v, %v", product, err)
	}
	dish, err := runtime.DefineDish(ctx, contract.DefineDishRequest{CommandID: "command-dish", DishID: "soup", Name: "Soup", Ingredients: []contract.Ingredient{{ProductID: "milk", Quantity: 1000}}})
	if err != nil || dish.DishID != "soup" {
		t.Fatalf("DefineDish() = %+v, %v", dish, err)
	}
	createdAt := now
	receipt, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{
		CommandID:  "command-delivery",
		DeliveryID: "delivery-1",
		Number:     "D-1",
		CreatedAt:  createdAt,
		Lines:      []contract.DeliveryLine{{ProductID: "milk", ManufacturedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 3000}},
	})
	if err != nil || receipt.DeliveryID != "delivery-1" || len(receipt.BatchIDs) != 1 {
		t.Fatalf("ReceiveDelivery() = %+v, %v", receipt, err)
	}
	orderEvaluation, err := runtime.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 7, DishID: "soup", Portions: 1}}})
	if err != nil || len(orderEvaluation.AcceptedPositions) != 1 {
		t.Fatalf("EvaluateOrder() = %+v, %v", orderEvaluation, err)
	}
	committed, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "command-order", ProposalID: "proposal-1", AcceptedPositions: orderEvaluation.AcceptedPositions})
	if err != nil || committed.Kind != contract.CommitResultOrderCommitted || committed.Order == nil {
		t.Fatalf("CommitAcceptedOrder() = %+v, %v", committed, err)
	}
	if _, err := runtime.EvaluateMenu(ctx); err != nil {
		t.Fatalf("EvaluateMenu() error = %v", err)
	}
	if _, err := runtime.GetInventory(ctx); err != nil {
		t.Fatalf("GetInventory() error = %v", err)
	}
	if _, err := runtime.GetExpiringBatches(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatalf("GetExpiringBatches() error = %v", err)
	}
	if _, err := runtime.GetProducts(ctx); err != nil {
		t.Fatalf("GetProducts() error = %v", err)
	}
	if _, err := runtime.GetDishCatalog(ctx); err != nil {
		t.Fatalf("GetDishCatalog() error = %v", err)
	}
	if _, err := runtime.GetDish(ctx, "soup"); err != nil {
		t.Fatalf("GetDish() error = %v", err)
	}
	if _, err := runtime.GetHistory(ctx); err != nil {
		t.Fatalf("GetHistory() error = %v", err)
	}
	if _, found, err := runtime.FindOrderByProposal(ctx, "proposal-1"); err != nil || !found {
		t.Fatalf("FindOrderByProposal() found=%v, err=%v", found, err)
	}
	if _, found, err := runtime.FindOrderByProposal(ctx, "unknown"); err != nil || found {
		t.Fatalf("FindOrderByProposal(unknown) found=%v, err=%v", found, err)
	}
	if _, err := runtime.ExpireBatch(ctx, contract.ExpireBatchRequest{CommandID: "expiration/" + receipt.BatchIDs[0], BatchID: receipt.BatchIDs[0]}); !errors.Is(err, contract.ErrNotDue) {
		t.Fatalf("ExpireBatch(before expiry) error = %v, want ErrNotDue", err)
	}

	now = now.Add(48 * time.Hour)
	expired, err := runtime.ProcessDueExpirations(ctx)
	if err != nil || len(expired) != 1 || expired[0].BatchID != receipt.BatchIDs[0] {
		t.Fatalf("ProcessDueExpirations() = %+v, %v", expired, err)
	}
	writtenOff, err := runtime.ExpireBatch(ctx, contract.ExpireBatchRequest{CommandID: "expiration/" + receipt.BatchIDs[0], BatchID: receipt.BatchIDs[0]})
	if err != nil || writtenOff.BatchID != receipt.BatchIDs[0] {
		t.Fatalf("ExpireBatch(replay after due) = %+v, %v", writtenOff, err)
	}
	if ran, err := runtime.RunSnapshotOnce(ctx); err != nil || !ran {
		t.Fatalf("RunSnapshotOnce() = %v, %v", ran, err)
	}
}

func TestRuntimeCommandLookupsPreserveTaggedResultsAndCopies(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	nextID := 0
	runtime, err := NewInMemory(Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			nextID++
			return fmt.Sprintf("generated-%d", nextID)
		},
		EventsPerSnapshot: 100,
	})
	if err != nil {
		t.Fatalf("NewInMemory() error = %v", err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "command-product", ProductID: "milk", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatalf("RegisterProduct() error = %v", err)
	}
	if _, err := runtime.DefineDish(ctx, contract.DefineDishRequest{CommandID: "command-dish", DishID: "soup", Name: "Soup", Ingredients: []contract.Ingredient{{ProductID: "milk", Quantity: 1000}}}); err != nil {
		t.Fatalf("DefineDish() error = %v", err)
	}
	receipt, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{
		CommandID: "command-delivery", DeliveryID: "delivery-1", Number: "D-1", CreatedAt: now,
		Lines: []contract.DeliveryLine{{ProductID: "milk", ManufacturedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 3000}},
	})
	if err != nil {
		t.Fatalf("ReceiveDelivery() error = %v", err)
	}
	evaluation, err := runtime.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 7, DishID: "soup", Portions: 1}}})
	if err != nil {
		t.Fatalf("EvaluateOrder() error = %v", err)
	}
	if _, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "command-order", ProposalID: "proposal-1", AcceptedPositions: evaluation.AcceptedPositions}); err != nil {
		t.Fatalf("CommitAcceptedOrder() error = %v", err)
	}
	now = now.Add(48 * time.Hour)
	if _, err := runtime.ProcessDueExpirations(ctx); err != nil {
		t.Fatalf("ProcessDueExpirations() error = %v", err)
	}
	if err := runtime.RunSnapshotWorker(cancelledContext(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSnapshotWorker(cancelled) error = %v, want context.Canceled", err)
	}

	expectedKinds := map[string]contract.CommandResultKind{
		"command-product":                   contract.CommandResultProductRegistered,
		"command-dish":                      contract.CommandResultDishDefined,
		"command-delivery":                  contract.CommandResultDeliveryReceived,
		"command-order":                     contract.CommandResultOrderPlaced,
		"expiration/" + receipt.BatchIDs[0]: contract.CommandResultBatchExpired,
	}
	expiry, _, err := runtime.FindRecordedCommand(ctx, "expiration/"+receipt.BatchIDs[0])
	if err != nil || expiry.Result.Expiry == nil || expiry.Result.Expiry.BatchID != receipt.BatchIDs[0] {
		t.Fatalf("FindRecordedCommand(expiry) = %+v, %v", expiry.Result.Expiry, err)
	}
	for commandID, expected := range expectedKinds {
		record, found, err := runtime.FindRecordedCommand(ctx, commandID)
		if err != nil || !found {
			t.Fatalf("FindRecordedCommand(%q) found=%v, err=%v", commandID, found, err)
		}
		if record.Result.Kind != expected {
			t.Errorf("FindRecordedCommand(%q).Kind = %q, want %q", commandID, record.Result.Kind, expected)
		}
		if count := populatedCommandPayloads(record.Result); count != 1 {
			t.Errorf("FindRecordedCommand(%q) has %d typed payloads, want exactly one", commandID, count)
		}
		switch expected {
		case contract.CommandResultProductRegistered:
			if record.Result.Product == nil || record.Result.Product.ProductID != "milk" || record.Result.Product.BaseUnit != contract.BaseUnitML {
				t.Errorf("product result = %+v", record.Result.Product)
			}
		case contract.CommandResultDishDefined:
			if record.Result.Dish == nil || record.Result.Dish.DishID != "soup" || len(record.Result.Dish.Ingredients) != 1 || record.Result.Dish.Ingredients[0].ProductID != "milk" || record.Result.Dish.Ingredients[0].Quantity != 1000 || record.Result.Dish.Ingredients[0].BaseUnit != "" {
				t.Errorf("dish result = %+v", record.Result.Dish)
			}
		case contract.CommandResultDeliveryReceived:
			if record.Result.Delivery == nil || record.Result.Delivery.DeliveryID != "delivery-1" || len(record.Result.Delivery.BatchIDs) != 1 || record.Result.Delivery.BatchIDs[0] != receipt.BatchIDs[0] {
				t.Errorf("delivery result = %+v", record.Result.Delivery)
			}
		case contract.CommandResultOrderPlaced:
			if record.Result.Order == nil || record.Result.Order.ProposalID != "proposal-1" || len(record.Result.Order.AcceptedPositions) != 1 || record.Result.Order.AcceptedPositions[0].LineNo != 7 || len(record.Result.Order.Allocations) != 1 || record.Result.Order.Allocations[0].Quantity != 1000 {
				t.Errorf("order result = %+v", record.Result.Order)
			}
		case contract.CommandResultBatchExpired:
			if record.Result.Expiry == nil || record.Result.Expiry.BatchID != receipt.BatchIDs[0] || record.Result.Expiry.WrittenOffQuantity != 2000 || record.Result.Expiry.Reason == "" {
				t.Errorf("expiry result = %+v", record.Result.Expiry)
			}
		}
	}

	dish, found, err := runtime.FindRecordedCommand(ctx, "command-dish")
	if err != nil || !found || dish.Result.Dish == nil || len(dish.Result.Dish.Ingredients) != 1 {
		t.Fatalf("FindRecordedCommand(dish) = %+v, found=%v, err=%v", dish, found, err)
	}
	dish.Result.Dish.Ingredients[0].Quantity = 9000
	dishAgain, _, err := runtime.FindRecordedCommand(ctx, "command-dish")
	if err != nil || dishAgain.Result.Dish.Ingredients[0].Quantity != 1000 {
		t.Fatalf("dish lookup was not detached: %+v, %v", dishAgain.Result.Dish, err)
	}

	delivery, _, err := runtime.FindRecordedCommand(ctx, "command-delivery")
	if err != nil || delivery.Result.Delivery == nil || len(delivery.Result.Delivery.BatchIDs) != 1 {
		t.Fatalf("FindRecordedCommand(delivery) = %+v, %v", delivery.Result.Delivery, err)
	}
	delivery.Result.Delivery.BatchIDs[0] = "changed"
	deliveryAgain, _, err := runtime.FindRecordedCommand(ctx, "command-delivery")
	if err != nil || deliveryAgain.Result.Delivery.BatchIDs[0] != receipt.BatchIDs[0] {
		t.Fatalf("delivery lookup was not detached: %+v, %v", deliveryAgain.Result.Delivery, err)
	}

	order, found, err := runtime.FindOrderByProposal(ctx, "proposal-1")
	if err != nil || !found || len(order.AcceptedPositions) != 1 || len(order.Allocations) != 1 {
		t.Fatalf("FindOrderByProposal() = %+v, found=%v, err=%v", order, found, err)
	}
	order.AcceptedPositions[0].LineNo = 99
	order.Allocations[0].Quantity = 9999
	orderAgain, _, err := runtime.FindOrderByProposal(ctx, "proposal-1")
	if err != nil || orderAgain.AcceptedPositions[0].LineNo != 7 || orderAgain.Allocations[0].Quantity != 1000 {
		t.Fatalf("order lookup was not detached: %+v, %v", orderAgain, err)
	}

	orderRecord, _, err := runtime.FindRecordedCommand(ctx, "command-order")
	if err != nil || orderRecord.Result.Order == nil {
		t.Fatalf("FindRecordedCommand(order) = %+v, %v", orderRecord.Result.Order, err)
	}
	orderRecord.Result.Order.AcceptedPositions[0].LineNo = 88
	orderRecord.Result.Order.Allocations[0].Quantity = 8888
	orderRecordAgain, _, err := runtime.FindRecordedCommand(ctx, "command-order")
	if err != nil || orderRecordAgain.Result.Order.AcceptedPositions[0].LineNo != 7 || orderRecordAgain.Result.Order.Allocations[0].Quantity != 1000 {
		t.Fatalf("recorded order result was not detached: %+v, %v", orderRecordAgain.Result.Order, err)
	}

	if _, found, err := runtime.FindRecordedCommand(ctx, "unknown-command"); err != nil || found {
		t.Fatalf("FindRecordedCommand(unknown) found=%v, err=%v", found, err)
	}
}

func TestRecordedDishLookupDoesNotRequireCatalogRead(t *testing.T) {
	ctx := context.Background()
	store := &versionFailureStore{EventStore: memory.NewEventStore()}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	nextID := 0
	owner, err := application.NewOwner(store, func() time.Time { return now }, func() string {
		nextID++
		return fmt.Sprintf("generated-%d", nextID)
	})
	if err != nil {
		t.Fatalf("NewOwner() error = %v", err)
	}
	if err := owner.ConfigureSnapshots(memory.NewSnapshotStore(), 10); err != nil {
		t.Fatalf("ConfigureSnapshots() error = %v", err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "register", ProductID: "milk", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatalf("RegisterProduct() error = %v", err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "define", DishID: "soup", Name: "Soup", Ingredients: []contract.Ingredient{{ProductID: "milk", Quantity: 1000}}}); err != nil {
		t.Fatalf("DefineDish() error = %v", err)
	}
	if record, found, err := store.FindCommand(ctx, "define"); err != nil || !found || record.Result.Dish == nil {
		t.Fatalf("FindCommand() found=%v result=%+v err=%v; want committed dish fact", found, record.Result.Dish, err)
	}

	runtime := &Runtime{owner: owner}
	store.failVersion = true
	record, found, err := runtime.FindRecordedCommand(ctx, "define")
	if err != nil || !found {
		t.Fatalf("FindRecordedCommand() found=%v err=%v; want journal result despite unavailable catalog projection", found, err)
	}
	if record.Result.Dish == nil || record.Result.Dish.DishID != "soup" || len(record.Result.Dish.Ingredients) != 1 {
		t.Fatalf("FindRecordedCommand() dish = %+v", record.Result.Dish)
	}
	ingredient := record.Result.Dish.Ingredients[0]
	if ingredient.ProductID != "milk" || ingredient.Quantity != 1000 || ingredient.BaseUnit != "" {
		t.Fatalf("recorded ingredient = %+v, want fact fields with optional BaseUnit unset", ingredient)
	}
}

func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func populatedCommandPayloads(result contract.CommandResult) int {
	count := 0
	if result.Product != nil {
		count++
	}
	if result.Dish != nil {
		count++
	}
	if result.Delivery != nil {
		count++
	}
	if result.Order != nil {
		count++
	}
	if result.Expiry != nil {
		count++
	}
	return count
}
