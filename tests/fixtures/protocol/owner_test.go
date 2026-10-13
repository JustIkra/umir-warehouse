package protocol

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW6RealOwnerFaultsAndRetainedResults(t *testing.T) {
	for _, point := range []string{"before-commit", "after-real-commit-return-unknown", "after-durable-before-reply"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("MSK", 10800))
			scope := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
			reg, err := bootstrap.NewRegistry(bootstrap.Options{Now: func() time.Time { return at }, NewID: rand.Text, EventsPerSnapshot: 100}, scope.RuntimeEpoch)
			if err != nil {
				t.Fatal(err)
			}
			if err = reg.Register(ctx, scope); err != nil {
				t.Fatal(err)
			}
			f := NewOwnerFaults(time.Now)
			id := "77777777-7777-4777-8777-777777777777"
			if _, err = f.Arm(FaultArm{id, point, "registerProduct", "faulted", 30000, 1}); err != nil {
				t.Fatal(err)
			}
			directory := NewDirectory(reg, f)
			r, err := directory.RuntimeFor(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			q := contract.RegisterProductRequest{CommandID: "other", ProductID: "other", Name: "Other", BaseUnit: contract.BaseUnitG}
			if _, err = r.RegisterProduct(ctx, q); err != nil {
				t.Fatal(err)
			}
			q.CommandID = "faulted"
			q.ProductID = "P"
			q.Name = "Product"
			state := &requestState{}
			v, err := r.RegisterProduct(context.WithValue(ctx, requestStateKey{}, state), q)
			if point == "before-commit" {
				if err == nil {
					t.Fatal("before commit did not refuse")
				}
			} else if point == "after-real-commit-return-unknown" {
				if !errors.Is(err, contract.ErrUnknownWriteOutcome) {
					t.Fatal(err)
				}
			} else if err != nil || !state.drop || v.ProductID != "P" {
				t.Fatalf("reply barrier not bound: %v %+v", err, state)
			}
			obs, e := f.Read(id)
			if e != nil || obs.State != "hit" || obs.Hits != 1 {
				t.Fatalf("fault observation %+v %v", obs, e)
			}
			record, found, e := r.FindRecordedCommand(ctx, q.CommandID)
			if e != nil {
				t.Fatal(e)
			}
			if point == "before-commit" {
				if found || obs.OwnerOutcome != "not_committed" {
					t.Fatal("invented commit")
				}
			} else if !found || record.Result.Product == nil || record.Result.Product.ProductID != "P" || obs.OwnerOutcome != "committed" {
				t.Fatalf("real commit missing %+v", record)
			}
			recovered, e := r.RegisterProduct(ctx, q)
			if e != nil || recovered.ProductID != "P" {
				t.Fatalf("exact retry %+v %v", recovered, e)
			}
			products, e := r.GetProducts(ctx)
			if e != nil || len(products.Items) != 2 {
				t.Fatalf("duplicate or lost effect %+v %v", products, e)
			}
		})
	}
}
func TestW6FaultOnlyHitsOnceUnderConcurrency(t *testing.T) {
	f := NewOwnerFaults(time.Now)
	id := "77777777-7777-4777-8777-777777777777"
	if _, err := f.Arm(FaultArm{id, "before-commit", "registerProduct", "same", 30000, 1}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { f.hit("registerProduct", "same", "before-commit", "not_committed") })
	}
	wg.Wait()
	v, err := f.Read(id)
	if err != nil || v.Hits != 1 {
		t.Fatal(v, err)
	}
}
