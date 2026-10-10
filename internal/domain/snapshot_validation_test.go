package domain

import (
	"testing"
	"time"
)

func TestValidateSnapshotRequiresMatchingCommandResults(t *testing.T) {
	product := Product{ProductID: "product-1", Name: "Tomato", BaseUnit: BaseUnitG}
	snapshot := Snapshot{
		SchemaVersion: InventorySchemaV1,
		StreamID:      InventoryStreamID,
		LastSequence:  1,
		State: InventoryState{
			Products: map[string]Product{"product-1": product},
			Commands: map[string]CommandRecord{
				"command-1": {
					CommandID: "command-1", CommandType: "RegisterProduct", CommandFingerprint: "fingerprint",
					Result: CommandResult{Kind: CommandResultProductRegistered, Product: &product},
				},
			},
			StateVersion: 1,
		},
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("ValidateSnapshot(valid state) error = %v", err)
	}

	wrongResult := product
	wrongResult.Name = "Potato"
	snapshot.State.Commands["command-1"] = CommandRecord{
		CommandID: "command-1", CommandType: "RegisterProduct", CommandFingerprint: "fingerprint",
		Result: CommandResult{Kind: CommandResultProductRegistered, Product: &wrongResult},
	}
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("ValidateSnapshot accepted a command result that disagrees with InventoryState")
	}
}

func TestValidateInventoryStateRejectsNegativeBalancesAndBrokenOrderIndex(t *testing.T) {
	state := InventoryState{
		Products: map[string]Product{"product-1": {ProductID: "product-1", Name: "Tomato", BaseUnit: BaseUnitG}},
		Deliveries: map[string]Delivery{"delivery-1": {
			DeliveryID: "delivery-1", Number: "invoice-1", CreatedAt: validationTime(),
			Lines: []DeliveryLine{{ProductID: "product-1", ManufacturedAt: validationTime(), ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeDay}, ReceivedAt: validationTime(), Quantity: 1000}},
		}},
		Batches: map[string]Batch{"batch-1": {
			BatchID: "batch-1", DeliveryID: "delivery-1", LineNo: 1, ProductID: "product-1",
			ManufacturedAt: validationTime(), ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeDay}, ReceivedAt: validationTime(), ExpiresAt: validationTime().AddDate(0, 0, 2),
			ReceivedQuantity: 1000, RemainingQuantity: -1, ConsumedByOrders: 1001,
		}},
		Orders:           map[string]Order{},
		OrdersByProposal: map[string]string{"proposal-1": "missing-order"},
		StateVersion:     1,
	}
	if err := ValidateInventoryState(state); err == nil {
		t.Fatal("ValidateInventoryState accepted a negative batch balance")
	}
	batch := state.Batches["batch-1"]
	batch.RemainingQuantity = 1000
	batch.ConsumedByOrders = 0
	state.Batches["batch-1"] = batch
	if err := ValidateInventoryState(state); err == nil {
		t.Fatal("ValidateInventoryState accepted a broken proposal index")
	}
}

func validationTime() time.Time {
	return time.Date(2026, time.March, 1, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}
