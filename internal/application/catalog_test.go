package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestImmutableDefinitions(t *testing.T) {
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	ingredients := []contract.Ingredient{{ProductID: "p", Quantity: 500}}
	dish, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Pear", Ingredients: ingredients})
	if err != nil {
		t.Fatal(err)
	}
	ingredients[0].Quantity = 900
	dish.Ingredients[0].Quantity = 1000
	got, err := owner.GetDish(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	want := contract.Dish{DishID: "d", Name: "Pear", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500, BaseUnit: contract.BaseUnitG}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetDish() = %#v; want %#v", got, want)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p-2", ProductID: "p", Name: "Pear 2", BaseUnit: contract.BaseUnitG}); !errors.Is(err, contract.ErrProductAlreadyExists) {
		t.Fatalf("RegisterProduct duplicate error = %v; want ProductAlreadyExists", err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d-2", DishID: "d", Name: "Pear 2", Ingredients: want.Ingredients}); !errors.Is(err, contract.ErrDishAlreadyExists) {
		t.Fatalf("DefineDish duplicate error = %v; want DishAlreadyExists", err)
	}
}

func TestDefineDishValidatesOptionalUnitHint(t *testing.T) {
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	for _, hint := range []contract.BaseUnit{contract.BaseUnitML, "kg"} {
		_, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-" + string(hint), DishID: "d-" + string(hint), Name: "Pear", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500, BaseUnit: hint}}})
		if !errors.Is(err, contract.ErrInvalidArgument) {
			t.Fatalf("DefineDish(unit hint %q) error = %v; want InvalidArgument", hint, err)
		}
	}
	version, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version after invalid unit hints = %d; want only product event", version)
	}
}

func TestDishUnitHintParticipatesInCommandIdentity(t *testing.T) {
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Pear", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500, BaseUnit: contract.BaseUnitG}}}
	if _, err := owner.DefineDish(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Ingredients[0].BaseUnit = ""
	if _, err := owner.DefineDish(ctx, request); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("DefineDish(changed unit hint) error = %v; want CommandIdentityConflict", err)
	}
}

func TestCatalogueWithoutStock(t *testing.T) {
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	for _, request := range []contract.RegisterProductRequest{
		{CommandID: "product-z", ProductID: "z", Name: "Zucchini", BaseUnit: contract.BaseUnitG},
		{CommandID: "product-a", ProductID: "a", Name: "Apple", BaseUnit: contract.BaseUnitPCS},
	} {
		if _, err := owner.RegisterProduct(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range []contract.DefineDishRequest{
		{CommandID: "dish-z", DishID: "z", Name: "Z dish", Ingredients: []contract.Ingredient{{ProductID: "z", Quantity: 1000}}},
		{CommandID: "dish-a", DishID: "a", Name: "A dish", Ingredients: []contract.Ingredient{{ProductID: "a", Quantity: 1000}}},
	} {
		if _, err := owner.DefineDish(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	products, err := owner.GetProducts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(products.Items) != 2 || products.Items[0].ProductID != "a" || products.Items[1].ProductID != "z" {
		t.Fatalf("GetProducts() = %#v; want registered products sorted by ID", products.Items)
	}
	catalog, err := owner.GetDishCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 2 || catalog.Items[0].DishID != "a" || catalog.Items[1].DishID != "z" {
		t.Fatalf("GetDishCatalog() = %#v; want dishes sorted by ID", catalog.Items)
	}
	if _, err := owner.GetDish(ctx, "missing"); !errors.Is(err, contract.ErrDishNotFound) {
		t.Fatalf("GetDish(unknown) error = %v; want DishNotFound", err)
	}
}

func TestCatalogueReadDoesNotProcessDueExpiration(t *testing.T) {
	now := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, now, 10000, 0)
	clock := &testClock{now: now}
	owner, err := NewOwner(store, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	products, err := owner.GetProducts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(products.Items) != 1 || products.StateVersion != 3 {
		t.Fatalf("GetProducts() = %#v; want one product at version 3", products)
	}
	version, err := store.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != 3 || owner.state.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("catalogue read changed due inventory: version=%d remaining=%d", version, owner.state.Batches["batch"].RemainingQuantity)
	}
}

func TestDeliveryAllOrNothing(t *testing.T) {
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	versionBefore, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := deliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9), []contract.DeliveryLine{
		{ProductID: "p", ManufacturedAt: moscowTime(2026, time.April, 1, 9), ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}, ReceivedAt: moscowTime(2026, time.April, 2, 9), Quantity: 1000},
		{ProductID: "unknown", ManufacturedAt: moscowTime(2026, time.April, 1, 9), ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}, ReceivedAt: moscowTime(2026, time.April, 2, 9), Quantity: 1000},
	})
	if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrProductNotFound) {
		t.Fatalf("ReceiveDelivery() error = %v; want ProductNotFound", err)
	}
	versionAfter, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("version after invalid delivery = %d; want unchanged %d", versionAfter, versionBefore)
	}
	if _, found, err := store.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("invalid delivery command = found %v, error %v; want no recorded command", found, err)
	}
}

func TestDeliveryRejectsInvalidBusinessTimes(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	valid := validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9))
	invalid := []contract.ReceiveDeliveryRequest{valid, valid, valid, valid}
	invalid[0].CreatedAt = now.Add(time.Minute)
	invalid[1].Lines[0].ManufacturedAt = invalid[1].Lines[0].ReceivedAt.Add(time.Minute)
	invalid[2].Lines[0].ReceivedAt = now.Add(time.Minute)
	invalid[3].Lines[0].ReceivedAt = invalid[3].Lines[0].ReceivedAt.In(time.FixedZone("UTC", 0))
	for index, request := range invalid {
		request.CommandID = []string{"invalid-delivery-a", "invalid-delivery-b", "invalid-delivery-c", "invalid-delivery-d"}[index]
		if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrInvalidArgument) {
			t.Fatalf("ReceiveDelivery(invalid time case %d) error = %v; want InvalidArgument", index, err)
		}
	}
	version, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version after invalid business times = %d; want only product event", version)
	}
}

func TestDuplicateDeliveryID(t *testing.T) {
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := validDeliveryRequest("delivery-cmd-1", "delivery-1", moscowTime(2026, time.April, 2, 9))
	first, err := owner.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.CommandID = "delivery-cmd-2"
	if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrDeliveryAlreadyReceived) {
		t.Fatalf("ReceiveDelivery(duplicate deliveryID) error = %v; want DeliveryAlreadyReceived", err)
	}
	if _, found, err := store.FindCommand(ctx, request.CommandID); err != nil || found {
		t.Fatalf("duplicate command = found %v, error %v; want no recorded command", found, err)
	}
	replayed, err := owner.ReceiveDelivery(ctx, validDeliveryRequest("delivery-cmd-1", "delivery-1", moscowTime(2026, time.April, 2, 9)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("exact repeat = %#v; want original receipt %#v", replayed, first)
	}
}

func TestDuplicateDeliveryIDPrecedesBodyValidation(t *testing.T) {
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ReceiveDelivery(ctx, validDeliveryRequest("delivery-cmd-1", "delivery-1", moscowTime(2026, time.April, 2, 9))); err != nil {
		t.Fatal(err)
	}
	valid := validDeliveryRequest("unused", "delivery-1", moscowTime(2026, time.April, 2, 9))
	requests := []contract.ReceiveDeliveryRequest{valid, valid, valid, valid}
	requests[0].Number = ""
	requests[1].Lines = nil
	requests[2].CreatedAt = requests[2].CreatedAt.In(time.FixedZone("UTC", 0))
	requests[3].CreatedAt = moscowTime(2026, time.April, 2, 11)
	ids := []string{"duplicate-no-number", "duplicate-no-lines", "duplicate-wrong-offset", "duplicate-future-created"}
	for index, request := range requests {
		request.CommandID = ids[index]
		if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrDeliveryAlreadyReceived) {
			t.Fatalf("ReceiveDelivery(duplicate with invalid body %d) error = %v; want DeliveryAlreadyReceived", index, err)
		}
	}
}

func TestPendingExactRetriesReturnRecordedResults(t *testing.T) {
	ctx := context.Background()
	t.Run("product", func(t *testing.T) {
		base := memory.NewEventStore()
		store := &delayedCommitStore{EventStore: base, commandID: "product-p"}
		owner := newOwnerForCatalogTest(t, store, moscowTime(2026, time.April, 2, 10))
		request := contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}
		if _, err := owner.RegisterProduct(ctx, request); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
			t.Fatalf("first RegisterProduct() error = %v; want unknown write", err)
		}
		got, err := owner.RegisterProduct(ctx, request)
		if err != nil || got != (contract.Product{ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}) {
			t.Fatalf("exact RegisterProduct retry = %#v, %v; want original product", got, err)
		}
	})
	t.Run("dish", func(t *testing.T) {
		base := memory.NewEventStore()
		store := &delayedCommitStore{EventStore: base, commandID: "dish-d"}
		owner := newOwnerForCatalogTest(t, store, moscowTime(2026, time.April, 2, 10))
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
		request := contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Pear dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500}}}
		if _, err := owner.DefineDish(ctx, request); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
			t.Fatalf("first DefineDish() error = %v; want unknown write", err)
		}
		got, err := owner.DefineDish(ctx, request)
		want := contract.Dish{DishID: "d", Name: "Pear dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 500, BaseUnit: contract.BaseUnitG}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("exact DefineDish retry = %#v, %v; want original dish %#v", got, err, want)
		}
	})
	t.Run("delivery", func(t *testing.T) {
		base := memory.NewEventStore()
		store := &delayedCommitStore{EventStore: base, commandID: "delivery-cmd"}
		owner := newOwnerForCatalogTest(t, store, moscowTime(2026, time.April, 2, 10))
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
		request := validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9))
		if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
			t.Fatalf("first ReceiveDelivery() error = %v; want unknown write", err)
		}
		got, err := owner.ReceiveDelivery(ctx, request)
		if err != nil || got.DeliveryID != "delivery-1" || len(got.BatchIDs) != 1 {
			t.Fatalf("exact ReceiveDelivery retry = %#v, %v; want original receipt", got, err)
		}
	})
}

func TestPendingExactRetryReturnsResultWhenProjectionRepairFails(t *testing.T) {
	ctx := context.Background()
	base := memory.NewEventStore()
	store := &delayedCommitStore{EventStore: base, commandID: "delivery-cmd", corruptResultBatch: true}
	owner := newOwnerForCatalogTest(t, store, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9))
	if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
		t.Fatalf("first ReceiveDelivery() error = %v; want unknown write", err)
	}
	got, err := owner.ReceiveDelivery(ctx, request)
	if err != nil || got.DeliveryID != "delivery-1" || len(got.BatchIDs) != 1 {
		t.Fatalf("exact retry after projection repair failure = %#v, %v; want recorded receipt", got, err)
	}
	if owner.usable {
		t.Fatal("projection should remain unavailable after failed repair")
	}
}

func TestDeliveryFingerprintCanonicalizesTimesAndPreservesLineOrder(t *testing.T) {
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9))
	request.Lines = append(request.Lines, request.Lines[0])
	request.Lines[1].Quantity = 2000
	first, err := owner.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Lines[0].ManufacturedAt = request.Lines[0].ManufacturedAt.In(time.FixedZone("same-offset", 3*60*60))
	request.Lines[0].ReceivedAt = request.Lines[0].ReceivedAt.In(time.FixedZone("same-offset", 3*60*60))
	replayed, err := owner.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatalf("ReceiveDelivery(equivalent canonical times) error = %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("canonical-time replay = %#v; want original receipt %#v", replayed, first)
	}
	request.Lines[0], request.Lines[1] = request.Lines[1], request.Lines[0]
	if _, err := owner.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("ReceiveDelivery(reordered lines) error = %v; want CommandIdentityConflict", err)
	}
}

func TestNonExpiryCommandsCannotUseExpirationNamespace(t *testing.T) {
	owner, _, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	_, err := owner.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "expiration/product", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG})
	if !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatalf("RegisterProduct(reserved command ID) error = %v; want InvalidArgument", err)
	}
}

func TestExpiredArrivalAtomic(t *testing.T) {
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := deliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 10), []contract.DeliveryLine{{
		ProductID: "p", ManufacturedAt: moscowTime(2026, time.April, 1, 9), ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}, ReceivedAt: moscowTime(2026, time.April, 2, 9), Quantity: 2000,
	}})
	receipt, err := owner.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.BatchIDs) != 1 {
		t.Fatalf("receipt batch IDs = %#v; want one ID", receipt.BatchIDs)
	}
	version, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("event version = %d; want product + delivery + expiry = 3", version)
	}
	batches, err := store.ReadBatches(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0].Events) != 2 || batches[0].Events[0].Type != domain.EventDeliveryReceived || batches[0].Events[1].Type != domain.EventBatchExpired {
		t.Fatalf("delivery event batch = %#v; want one atomic DeliveryReceived + BatchExpired command", batches)
	}
	state, err := owner.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.State.Batches[receipt.BatchIDs[0]].RemainingQuantity; got != 0 {
		t.Fatalf("expired incoming remaining = %d; want 0 in final projection", got)
	}
	if got := state.State.Batches[receipt.BatchIDs[0]].WrittenOffForExpiry; got != 2000 {
		t.Fatalf("expired incoming write-off = %d; want 2000", got)
	}
}

func TestDeliveryResolvesUnknownWriteWithOriginalBatch(t *testing.T) {
	ctx := context.Background()
	base := memory.NewEventStore()
	clock := &testClock{now: moscowTime(2026, time.April, 2, 10)}
	wrapper := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if batch.CommandType == "ReceiveDelivery" && err == nil {
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
		return result, err
	}}
	owner, err := NewOwner(wrapper, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request := validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9))
	receipt, err := owner.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.BatchIDs) != 1 {
		t.Fatalf("resolved delivery receipt = %#v; want one original batch ID", receipt)
	}
	version, err := base.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("version after unknown write resolution = %d; want one product and one delivery", version)
	}
	command, found, err := base.FindCommand(ctx, request.CommandID)
	if err != nil || !found || command.Result.Delivery == nil || !reflect.DeepEqual(command.Result.Delivery.BatchIDs, receipt.BatchIDs) {
		t.Fatalf("recorded delivery = %#v, found %v, error %v; want returned original batch ID", command, found, err)
	}
}

func TestDeliveryReturnsKnownCommitWhenProjectionFails(t *testing.T) {
	ctx := context.Background()
	base := memory.NewEventStore()
	clock := &testClock{now: moscowTime(2026, time.April, 2, 10)}
	wrapper := &eventStoreWrapper{EventStore: base, append: func(batch domain.CommandBatch, result domain.AppendResult, err error) (domain.AppendResult, error) {
		if batch.CommandType == "ReceiveDelivery" && err == nil {
			result.Batch.Events[0].Type = domain.EventBatchExpired
		}
		return result, err
	}}
	owner, err := NewOwner(wrapper, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	receipt, err := owner.ReceiveDelivery(ctx, validDeliveryRequest("delivery-cmd", "delivery-1", moscowTime(2026, time.April, 2, 9)))
	if err != nil || len(receipt.BatchIDs) != 1 {
		t.Fatalf("ReceiveDelivery() = %#v, %v; want committed receipt despite projection failure", receipt, err)
	}
	if owner.usable {
		t.Fatal("projection remained usable after apply failure")
	}
	if _, err := owner.GetProducts(ctx); err != nil {
		t.Fatalf("GetProducts() could not recover committed projection: %v", err)
	}
}

func TestConcurrentSameDelivery(t *testing.T) {
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, moscowTime(2026, time.April, 2, 10))
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	versionBefore, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := validDeliveryRequest("delivery-cmd-a", "delivery-shared", moscowTime(2026, time.April, 2, 9))
	requests := []contract.ReceiveDeliveryRequest{base, base}
	requests[1].CommandID = "delivery-cmd-b"
	results := make(chan error, len(requests))
	for _, request := range requests {
		request := request
		go func() {
			_, err := owner.ReceiveDelivery(ctx, request)
			results <- err
		}()
	}
	var accepted, duplicated int
	for range requests {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, contract.ErrDeliveryAlreadyReceived):
			duplicated++
		default:
			t.Fatalf("concurrent ReceiveDelivery() error = %v; want one success and one duplicate", err)
		}
	}
	if accepted != 1 || duplicated != 1 {
		t.Fatalf("concurrent results success=%d duplicate=%d; want one each", accepted, duplicated)
	}
	versionAfter, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore+1 {
		t.Fatalf("version after concurrent delivery = %d; want one event after %d", versionAfter, versionBefore)
	}
}

func newCatalogOwner(t *testing.T, now time.Time) (*Owner, *memory.EventStore, *testClock) {
	t.Helper()
	store := memory.NewEventStore()
	clock := &testClock{now: now}
	owner, err := NewOwner(store, clock.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	return owner, store, clock
}

func newOwnerForCatalogTest(t *testing.T, store EventStore, now time.Time) *Owner {
	t.Helper()
	owner, err := NewOwner(store, func() time.Time { return now }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

type delayedCommitStore struct {
	EventStore
	commandID          string
	appendAttempts     int
	corruptResultBatch bool
}

func (s *delayedCommitStore) AppendBatch(ctx context.Context, command domain.CommandBatch) (domain.AppendResult, error) {
	if command.CommandID != s.commandID {
		return s.EventStore.AppendBatch(ctx, command)
	}
	s.appendAttempts++
	if s.appendAttempts <= 2 {
		return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
	}
	result, err := s.EventStore.AppendBatch(ctx, command)
	if err == nil && s.corruptResultBatch && len(result.Batch.Events) != 0 {
		result.Batch.Events[0].Type = domain.EventBatchExpired
	}
	return result, err
}

func validDeliveryRequest(commandID, deliveryID string, now time.Time) contract.ReceiveDeliveryRequest {
	manufactured := now.Add(-24 * time.Hour)
	return deliveryRequest(commandID, deliveryID, now, []contract.DeliveryLine{{
		ProductID: "p", ManufacturedAt: manufactured, ShelfLife: contract.ShelfLife{Value: 3, Unit: contract.ShelfLifeDay}, ReceivedAt: now, Quantity: 1000,
	}})
}

func deliveryRequest(commandID, deliveryID string, createdAt time.Time, lines []contract.DeliveryLine) contract.ReceiveDeliveryRequest {
	return contract.ReceiveDeliveryRequest{CommandID: commandID, DeliveryID: deliveryID, Number: "invoice", CreatedAt: createdAt, Lines: lines}
}
