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
				SchemaVersion: SnapshotSchemaV2,
				StreamID:      InventoryStreamID,
				LastSequence:  int64(batchCount),
				State:         benchhelpersInventoryState(batchCount),
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(batchCount), "batches/op")
			b.ReportMetric(float64(batchCount), "catalog-products/op")
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
		Products:     make(map[string]Product, batchCount),
		Dishes:       make(map[string]Dish, batchCount),
		DeliveryIDs:  make(map[string]struct{}, batchCount),
		Batches:      make(map[string]Batch, batchCount),
		StateVersion: int64(batchCount),
	}
	for i := 0; i < batchCount; i++ {
		suffix := strconv.Itoa(i)
		productID := "product-" + suffix
		dishID := "dish-" + suffix
		deliveryID := "delivery-" + suffix
		batchID := "batch-" + suffix
		product := Product{ProductID: productID, Name: "Tomato " + suffix, BaseUnit: BaseUnitG}
		state.Products[productID] = product
		state.Dishes[dishID] = Dish{DishID: dishID, Name: "Soup " + suffix, Ingredients: []Ingredient{
			{ProductID: productID, Quantity: 1500},
		}}
		state.DeliveryIDs[deliveryID] = struct{}{}
		state.Batches[batchID] = Batch{
			BatchID: batchID, DeliveryID: deliveryID, LineNo: 1, ProductID: productID,
			ManufacturedAt: manufacturedAt, ShelfLife: ShelfLife{Value: 2, Unit: ShelfLifeMonth},
			ReceivedAt: receivedAt, ExpiresAt: expiresAt, ReceivedQuantity: 10000,
			RemainingQuantity: 9000, ConsumedByOrders: 1000,
		}
	}
	return state
}

func BenchmarkApplyBatch(b *testing.B) {
	for _, orderCount := range []int{1000, 10_000} {
		b.Run(strconv.Itoa(orderCount), func(b *testing.B) {
			state := layerState(b, 50, orderCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				orderID := "bench-" + strconv.Itoa(i)
				batch := layerBatch(orderID, "ConfirmOrder", state.StateVersion+1, layerOrderEvent(orderID, "d1", "p1", "batch-"+strconv.Itoa(i%50)))
				if err := state.ApplyBatch(batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
