//go:build integration

package durable_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/adapters/rabbitmq"
	"github.com/JustIkra/umir-warehouse/internal/adapters/redis"
	"github.com/JustIkra/umir-warehouse/tests/itest"
)

func TestScopedTransactionDoesNotLeakToReusedConnection(t *testing.T) {
	ctx := t.Context()
	db, err := postgres.Open(ctx, postgres.Config{URL: itest.NewDatabase(t), MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	scope := postgres.Scope{Actor: postgres.ActorTenant, OrgID: "org-a", RestaurantID: "restaurant-a", UserID: "user-a"}
	want := [4]string{"tenant", "org-a", "restaurant-a", "user-a"}
	err = db.InScopedTx(ctx, scope, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var got [4]string
		if err := tx.QueryRow(ctx, "SELECT current_setting('umir.actor'), current_setting('umir.org_id'), current_setting('umir.restaurant_id'), current_setting('umir.user_id')").Scan(&got[0], &got[1], &got[2], &got[3]); err != nil {
			return err
		}
		if got != want {
			t.Errorf("scope = %q, want %q", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got [4]string
	err = db.Pool().QueryRow(ctx, "SELECT COALESCE(current_setting('umir.actor', true), ''), COALESCE(current_setting('umir.org_id', true), ''), COALESCE(current_setting('umir.restaurant_id', true), ''), COALESCE(current_setting('umir.user_id', true), '')").Scan(&got[0], &got[1], &got[2], &got[3])
	if err != nil {
		t.Fatal(err)
	}
	if got != [4]string{} {
		t.Fatalf("scope leaked outside transaction: %q", got)
	}
}

func TestBrokerTopologyIsIsolatedAndIdempotent(t *testing.T) {
	conn, err := amqp.Dial(itest.NewVHost(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	topology := rabbitmq.DefaultTopology(rabbitmq.TopologyOptions{Feedback: true})
	for range 2 {
		if err := rabbitmq.Declare(ch, topology); err != nil {
			t.Fatal(err)
		}
	}
	if err := rabbitmq.CheckPassive(ch, topology); err != nil {
		t.Fatal(err)
	}
}

func TestCacheSlotsEnforceAndReleaseWarehouseQuota(t *testing.T) {
	ctx := t.Context()
	url := itest.RedisCacheURL(t)
	client, err := redis.Open(ctx, redis.Config{URL: url, KeyRoot: itest.RedisPrefixOn(t, url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	slots, err := client.Slots(redis.OwnerWarehouse)
	if err != nil {
		t.Fatal(err)
	}
	request := redis.SlotRequest{Holder: "first", TTL: time.Minute, Dimensions: []redis.SlotDimension{{Key: "capslots:restaurant-a", MaxCount: 1}}}
	grant, err := slots.Acquire(ctx, request)
	if err != nil || !grant.Granted {
		t.Fatalf("first slot = %+v, err = %v", grant, err)
	}
	request.Holder = "second"
	grant, err = slots.Acquire(ctx, request)
	if err != nil || grant.Granted {
		t.Fatalf("full quota = %+v, err = %v", grant, err)
	}
	if err := slots.Release(ctx, "first", request.Dimensions[0].Key); err != nil {
		t.Fatal(err)
	}
	grant, err = slots.Acquire(ctx, request)
	if err != nil || !grant.Granted {
		t.Fatalf("released quota = %+v, err = %v", grant, err)
	}
}
