package domain

import "testing"

func TestW5SnapshotAuditRejectsChangedDeliveryCapturedReceipt(t *testing.T) {
	original := CommandResult{Kind: CommandResultDeliveryReceived, Delivery: &DeliveryReceipt{DeliveryID: "delivery", BatchIDs: []string{"batch"}, OrganizationID: "org", RestaurantID: "tenant", RuntimeEpoch: "epoch", LifecycleVersion: "1", EvaluatedAt: validationTime(), StateVersion: 2, Batches: []Batch{{BatchID: "batch", AppliedTimeZone: "Europe/Moscow", AppliedSettingsVersion: "1", TZDataVersion: "2025c", ExpiresAt: validationTime()}}}}
	for _, change := range []func(*DeliveryReceipt){func(v *DeliveryReceipt) { v.OrganizationID = "other" }, func(v *DeliveryReceipt) { v.RuntimeEpoch = "other" }, func(v *DeliveryReceipt) { v.StateVersion++ }, func(v *DeliveryReceipt) { v.Batches[0].ExpiresAt = v.Batches[0].ExpiresAt.AddDate(0, 0, 1) }, func(v *DeliveryReceipt) { v.Batches[0].AppliedSettingsVersion = "2" }} {
		modified := CloneCommandResult(original)
		change(modified.Delivery)
		if equalCommandResult(original, modified) {
			t.Error("changed retained delivery context accepted by snapshot equality")
		}
	}
}
