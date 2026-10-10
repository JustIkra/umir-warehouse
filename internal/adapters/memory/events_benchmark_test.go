package memory

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

const benchhelpersAppendCommandsPerBatch = 100

var benchAppendResultSink domain.AppendResult
var benchAppendSequenceSink int64

func BenchmarkEventStoreAppendBatch100Commands(b *testing.B) {
	batches := benchhelpersCommandBatches(benchhelpersAppendCommandsPerBatch)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(benchhelpersAppendCommandsPerBatch, "commands/op")
	b.ReportMetric(benchhelpersAppendCommandsPerBatch, "events/op")
	for i := 0; i < b.N; i++ {
		store := NewEventStore()
		var final domain.AppendResult
		for _, batch := range batches {
			result, err := store.AppendBatch(ctx, batch)
			if err != nil {
				b.Fatal(err)
			}
			final = result
		}
		benchAppendResultSink = final
		benchAppendSequenceSink = final.Batch.FinalSequence
	}
}

func benchhelpersCommandBatches(commandCount int) []domain.CommandBatch {
	moscow := time.FixedZone("MSK", 3*60*60)
	recordedAt := time.Date(2026, time.January, 2, 10, 0, 0, 0, moscow)
	manufacturedAt := time.Date(2026, time.January, 1, 9, 0, 0, 0, moscow)
	expiresAt := time.Date(2026, time.March, 1, 9, 0, 0, 0, moscow)
	batches := make([]domain.CommandBatch, commandCount)
	for i := 0; i < commandCount; i++ {
		suffix := strconv.Itoa(i)
		commandID := "bench-command-" + suffix
		fingerprint := "bench-fingerprint-" + suffix
		event := domain.Event{
			EventID: "bench-event-" + suffix, StreamID: domain.InventoryStreamID,
			SchemaVersion: domain.InventorySchemaV1, CommandID: commandID,
			CommandFingerprint: fingerprint, RecordedAt: recordedAt,
		}
		commandType := ""
		switch i % 3 {
		case 0:
			commandType = "RegisterProduct"
			event.Type = domain.EventProductRegistered
			event.Payload.Product = &domain.Product{ProductID: "bench-product-" + suffix, Name: "Tomato " + suffix, BaseUnit: domain.BaseUnitG}
		case 1:
			commandType = "DefineDish"
			event.Type = domain.EventDishDefined
			event.Payload.Dish = &domain.Dish{
				DishID: "bench-dish-" + suffix, Name: "Soup " + suffix,
				Ingredients: []domain.Ingredient{{ProductID: "bench-product-" + strconv.Itoa(i-1), Quantity: 1500}},
			}
		case 2:
			commandType = "ReceiveDelivery"
			deliveryID := "bench-delivery-" + suffix
			productID := "bench-product-" + strconv.Itoa(i-2)
			batchID := "bench-batch-" + suffix
			event.Type = domain.EventDeliveryReceived
			event.Payload.Delivery = &domain.Delivery{
				DeliveryID: deliveryID, Number: "invoice-" + suffix, CreatedAt: recordedAt,
				Lines: []domain.DeliveryLine{{
					ProductID: productID, ManufacturedAt: manufacturedAt,
					ShelfLife:  domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeMonth},
					ReceivedAt: recordedAt, Quantity: 10000,
				}},
			}
			event.Payload.Batches = []domain.Batch{{
				BatchID: batchID, DeliveryID: deliveryID, LineNo: 1, ProductID: productID,
				ManufacturedAt: manufacturedAt, ShelfLife: domain.ShelfLife{Value: 2, Unit: domain.ShelfLifeMonth},
				ReceivedAt: recordedAt, ExpiresAt: expiresAt,
				ReceivedQuantity: 10000, RemainingQuantity: 10000,
			}}
		}
		event.CommandID = commandID
		event.CommandFingerprint = fingerprint
		batches[i] = domain.CommandBatch{
			ExpectedVersion: int64(i), CommandID: commandID, CommandType: commandType,
			CommandFingerprint: fingerprint, Events: []domain.Event{event},
		}
	}
	return batches
}
