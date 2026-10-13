package contracts_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

type servingWarehouse interface {
	EvaluateOrder(context.Context, contract.EvaluateOrderRequest) (contract.OrderEvaluation, error)
	EvaluateMenu(context.Context) (contract.MenuEvaluation, error)
	CommitAcceptedOrder(context.Context, contract.CommitOrderRequest) (contract.CommitResult, error)
	FindOrderAttempt(context.Context, string, string) (contract.OrderAttempt, error)
	FindRecordedCommand(context.Context, string) (contract.CommandRecord, bool, error)
	FindOrderByProposal(context.Context, string) (contract.Order, bool, error)
	GetProducts(context.Context) (contract.Products, error)
	GetDishCatalog(context.Context) (contract.DishCatalog, error)
	GetDish(context.Context, string) (contract.DishDetails, error)
}

var _ servingWarehouse = (*bootstrap.Runtime)(nil)

func TestW6FactoryConsumerUsesPublicSurfaces(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("MSK", 10800))
	scope := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	registry, err := bootstrap.NewRegistry(bootstrap.Options{Now: func() time.Time { return clock }, NewID: rand.Text, EventsPerSnapshot: 100}, scope.RuntimeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.RegisterRuntime(ctx, scope, "77777777-7777-4777-8777-777777777777"); err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Resolve(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var consumer servingWarehouse = runtime
	if _, err = runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	products, err := consumer.GetProducts(ctx)
	if err != nil || len(products.Items) != 1 {
		t.Fatal(products, err)
	}
	products.Items[0].Name = "changed"
	again, err := consumer.GetProducts(ctx)
	if err != nil || again.Items[0].Name != "P" {
		t.Fatal("consumer owns mutable source")
	}
	other := scope
	other.RestaurantID = "33333333-3333-4333-8333-333333333334"
	if err = registry.Register(ctx, other); err != nil {
		t.Fatal(err)
	}
	neighbor, err := registry.Resolve(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	items, err := neighbor.GetProducts(ctx)
	if err != nil || len(items.Items) != 0 {
		t.Fatal("consumer crossed tenant")
	}
}
