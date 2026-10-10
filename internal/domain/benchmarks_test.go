package domain

import (
	"strconv"
	"testing"
	"time"
)

var benchInventoryStateSink InventoryState
var benchSnapshotSink Snapshot

func BenchmarkCloneInventoryState(b *testing.B) {
	for _, batchCount := range []int{100, 1000} {
		b.Run(strconv.Itoa(batchCount), func(b *testing.B) {
			state := benchhelpersInventoryState(batchCount)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(batchCount), "batches/op")
			b.ReportMetric(float64(batchCount), "catalog-products/op")
			b.ReportMetric(float64(batchCount), "commands/op")
			for i := 0; i < b.N; i++ {
				benchInventoryStateSink = CloneInventoryState(state)
			}
		})
	}
}

func BenchmarkCloneSnapshot(b *testing.B) {
	for _, batchCount := range []int{100, 1000} {
		b.Run(strconv.Itoa(batchCount), func(b *testing.B) {
			snapshot := Snapshot{
				SchemaVersion: InventorySchemaV1,
				StreamID:      InventoryStreamID,
				LastSequence:  int64(batchCount),
				State:         benchhelpersInventoryState(batchCount),
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(batchCount), "batches/op")
			b.ReportMetric(float64(batchCount), "catalog-products/op")
			b.ReportMetric(float64(batchCount), "commands/op")
			for i := 0; i < b.N; i++ {
				benchSnapshotSink = CloneSnapshot(snapshot)
			}
		})
	}
}

func benchhelpersInventoryState(batchCount int) InventoryState {
	moscow := time.FixedZone("MSK", 3*60*60)
	manufacturedAt := time.Date(2026, time.January, 1, 9, 0, 0, 0, moscow)
	receivedAt := time.Date(2026, time.January, 2, 10, 0, 0, 0, moscow)
	expiresAt := time.Date(2026, time.March, 1, 9, 0, 0, 0, moscow)
	state := InventoryState{
		Products:         make(map[string]Product, batchCount),
		Dishes:           make(map[string]Dish, batchCount),
		Deliveries:       make(map[string]Delivery, batchCount),
		Batches:          make(map[string]Batch, batchCount),
		Orders:           make(map[string]Order, batchCount),
		OrdersByProposal: make(map[string]string, batchCount),
		Commands:         make(map[string]CommandRecord, batchCount),
		StateVersion:     int64(batchCount),
	}
	for i := 0; i < batchCount; i++ {
		suffix := strconv.Itoa(i)
		productID := "product-" + suffix
		dishID := "dish-" + suffix
		deliveryID := "delivery-" + suffix
		batchID := "batch-" + suffix
		orderID := "order-" + suffix
		proposalID := "proposal-" + suffix
		commandID := "command-" + suffix
		product := Product{ProductID: productID, Name: "Tomato " + suffix, BaseUnit: BaseUnitG}
		state.Products[productID] = product
		state.Dishes[dishID] = Dish{DishID: dishID, Name: "Soup " + suffix, Ingredients: []Ingredient{
			{ProductID: productID, Quantity: 1500},
		}}
		state.Deliveries[deliveryID] = Delivery{DeliveryID: deliveryID, Number: "invoice-" + suffix, CreatedAt: receivedAt, Lines: []DeliveryLine{
			{ProductID: productID, ManufacturedAt: manufacturedAt, ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeMonth}, ReceivedAt: receivedAt, Quantity: 10000},
		}}
		state.Batches[batchID] = Batch{
			BatchID: batchID, DeliveryID: deliveryID, LineNo: 1, ProductID: productID,
			ManufacturedAt: manufacturedAt, ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeMonth},
			ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: 10000,
			RemainingQuantity: 9000, ConsumedByOrders: 1000,
		}
		state.Orders[orderID] = Order{
			OrderID: orderID, ProposalID: proposalID,
			AcceptedPositions: []AcceptedPosition{{LineNo: 1, DishID: dishID, Portions: 1}},
			Allocations:       []Allocation{{ProductID: productID, BatchID: batchID, Quantity: 1000}},
			PlacedAt:          receivedAt,
		}
		state.OrdersByProposal[proposalID] = orderID
		productResult := product
		state.Commands[commandID] = CommandRecord{
			CommandID: commandID, CommandType: "RegisterProduct", CommandFingerprint: "fingerprint-" + suffix,
			Result: CommandResult{Kind: CommandResultProductRegistered, Product: &productResult},
		}
	}
	return state
}
