//go:build integration

package warehousepg

import (
	"context"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/tests/itest"
	"github.com/jackc/pgx/v5"
)

func TestRuntimeRolePurgesClosedRestaurantOutboxTextID(t *testing.T) {
	ctx := t.Context()
	adminURL := itest.NewDatabase(t)
	admin, err := postgres.Open(ctx, postgres.Config{URL: adminURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := Migrate(ctx, admin); err != nil {
		t.Fatal(err)
	}
	organizationID, restaurantID := testUUID(t), testUUID(t)
	closedAt := time.Now().Add(-8 * 24 * time.Hour)
	if _, err := admin.Pool().Exec(ctx, `INSERT INTO restaurants
		(restaurant_id, organization_id, lifecycle_version, time_zone, settings_version, state,
		 coverage_from, registered_at, closed_at)
		VALUES ($1, $2, 2, 'Europe/Moscow', 1, 'closed', $3, $3, $4)`, restaurantID, organizationID, closedAt, closedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool().Exec(ctx, `INSERT INTO streams (restaurant_id, version, updated_at) VALUES ($1, 0, $2)`, restaurantID, closedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool().Exec(ctx, `INSERT INTO outbox
		(id, exchange, routing_key, body, org_id, restaurant_id, created_at, not_before)
		VALUES ($1, 'umir.events', 'warehouse.ProductRegistered', '{"eventID":"event-1","schemaVersion":1,"generation":"generation"}'::jsonb, $2, $3, $4, $4)`,
		testUUID(t), organizationID, restaurantID, closedAt); err != nil {
		t.Fatal(err)
	}
	role := "t_wh_purge_" + randomHex(t, 8)
	password := randomHex(t, 24)
	roleSQL := pgx.Identifier{role}.Sanitize()
	if _, err := admin.Pool().Exec(ctx, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Pool().Exec(cleanupCtx, "DROP OWNED BY "+roleSQL); err != nil {
			t.Errorf("drop purge role grants: %v", err)
			return
		}
		if _, err := admin.Pool().Exec(cleanupCtx, "DROP ROLE IF EXISTS "+roleSQL); err != nil {
			t.Errorf("drop purge role: %v", err)
		}
	})
	for _, statement := range []string{
		"GRANT USAGE ON SCHEMA public TO " + roleSQL,
		"GRANT SELECT ON restaurants, streams, commands, events, snapshots, order_attempts TO " + roleSQL,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, worker_leases TO " + roleSQL,
		"GRANT EXECUTE ON FUNCTION purge_restaurant(uuid) TO " + roleSQL,
	} {
		if _, err := admin.Pool().Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	dbURL, err := runtimeURL(adminURL, role, password)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := postgres.Open(ctx, postgres.Config{URL: dbURL, MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if err := postgres.AssertRuntimeRole(ctx, runtimeDB); err != nil {
		t.Fatal(err)
	}
	err = runtimeDB.InSystemTx(ctx, "retention test", pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT purge_restaurant($1::uuid)`, restaurantID)
		return err
	})
	if err != nil {
		t.Fatalf("purge with runtime role: %v", err)
	}
	var outboxCount, restaurantCount int
	if err := admin.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox WHERE restaurant_id = $1`, restaurantID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if err := admin.Pool().QueryRow(ctx, `SELECT count(*) FROM restaurants WHERE restaurant_id = $1`, restaurantID).Scan(&restaurantCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 || restaurantCount != 1 {
		t.Fatalf("purge left outbox=%d and restaurant=%d; want 0 and tombstone 1", outboxCount, restaurantCount)
	}
}
