//go:build integration

package warehousepg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/domain"
	"github.com/JustIkra/umir-warehouse/tests/itest"
	"github.com/JustIkra/umir-warehouse/tests/storecontract"
	"github.com/jackc/pgx/v5"
)

func TestPostgresStoresSatisfyStorageContracts(t *testing.T) {
	ctx := t.Context()
	db, err := postgres.Open(ctx, postgres.Config{URL: itest.NewDatabase(t), MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	fixture := storecontract.Fixture{
		New: func(tb *testing.T) storecontract.Stores { return newTestStores(tb, db) },
		Other: func(tb *testing.T, _ storecontract.Stores) storecontract.Stores {
			return newTestStores(tb, db)
		},
	}
	t.Run("event store", func(t *testing.T) { storecontract.RunEventStore(t, fixture) })
	t.Run("snapshot store", func(t *testing.T) { storecontract.RunSnapshotStore(t, fixture) })
}

func TestRuntimeRoleCannotReadAcrossRestaurantsOrLeakScope(t *testing.T) {
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
	generation, err := postgres.Generation(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	first := contract.Scope{OrganizationID: testUUID(t), RestaurantID: testUUID(t), RuntimeEpoch: generation, LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	second := contract.Scope{OrganizationID: first.OrganizationID, RestaurantID: testUUID(t), RuntimeEpoch: generation, LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	insertTenant(t, admin, first)
	insertTenant(t, admin, second)

	roleSuffix := randomHex(t, 8)
	role := "t_wh_runtime_" + roleSuffix
	password := randomHex(t, 24)
	if _, err := admin.Pool().Exec(ctx, `REVOKE CREATE ON SCHEMA public FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	roleSQL := pgx.Identifier{role}.Sanitize()
	if _, err := admin.Pool().Exec(ctx, "CREATE ROLE "+roleSQL+" LOGIN PASSWORD '"+password+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Pool().Exec(cleanupCtx, "DROP OWNED BY "+roleSQL); err != nil {
			t.Errorf("drop test role grants: %v", err)
			return
		}
		if _, err := admin.Pool().Exec(cleanupCtx, "DROP ROLE IF EXISTS "+roleSQL); err != nil {
			t.Errorf("drop test role: %v", err)
		}
	})
	if _, err := admin.Pool().Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+roleSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool().Exec(ctx, "GRANT SELECT ON restaurants, streams, commands, events, snapshots, order_attempts TO "+roleSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool().Exec(ctx, "GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, worker_leases TO "+roleSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool().Exec(ctx, "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "+roleSQL); err != nil {
		t.Fatal(err)
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
		t.Fatalf("AssertRuntimeRole() = %v", err)
	}

	for _, test := range []struct {
		name  string
		scope contract.Scope
		want  int
	}{{"restaurant A", first, 1}, {"restaurant B", second, 1}} {
		t.Run(test.name, func(t *testing.T) {
			var got int
			err := runtimeDB.InScopedTx(ctx, toPostgresScope(test.scope), pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM restaurants`).Scan(&got)
			})
			if err != nil || got != test.want {
				t.Fatalf("scoped restaurant count = %d, err = %v; want %d", got, err, test.want)
			}
		})
	}
	var unscoped int
	if err := runtimeDB.InTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM restaurants`).Scan(&unscoped)
	}); err != nil {
		t.Fatal(err)
	}
	if unscoped != 0 {
		t.Fatalf("unscoped restaurant count = %d, want 0", unscoped)
	}
}

func TestJournalReceiptAndOutboxCommitTogether(t *testing.T) {
	ctx := t.Context()
	db, err := postgres.Open(ctx, postgres.Config{URL: itest.NewDatabase(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	scope := newTestScope(t, db)
	stores, err := NewRestaurantStores(ctx, db, scope)
	if err != nil {
		t.Fatal(err)
	}
	batch := domain.CommandBatch{CommandID: "command-1", CommandType: "RegisterProduct", CommandFingerprint: "fingerprint-1", Events: []domain.Event{{
		EventID: "event-1", RecordedAt: time.Date(2026, time.October, 16, 10, 0, 0, 123456789, time.FixedZone("MSK", 3*60*60)),
		Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "product-1", Name: "PRIVATE PRODUCT NAME", BaseUnit: domain.BaseUnitG}},
	}}}
	if _, err := stores.Events.AppendBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var count int
	var routingKey string
	var body []byte
	if err := db.Pool().QueryRow(ctx, `SELECT count(*), min(routing_key), min(body::text) FROM outbox WHERE restaurant_id = $1`, scope.RestaurantID).Scan(&count, &routingKey, &body); err != nil {
		t.Fatal(err)
	}
	if count != 1 || routingKey != "warehouse.ProductRegistered" {
		t.Fatalf("outbox count/key = %d/%q; want 1/warehouse.ProductRegistered", count, routingKey)
	}
	var envelope eventEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.EventID != "event-1" || envelope.AggregateRevision != 1 || envelope.Generation != scope.RuntimeEpoch || len(envelope.Topics) != 2 {
		t.Fatalf("outbox envelope = %#v", envelope)
	}
	if bytes.Contains(body, []byte("PRIVATE PRODUCT NAME")) {
		t.Fatal("outbox envelope contains business payload data")
	}
	batch.CommandID = "command-2"
	batch.CommandFingerprint = "fingerprint-2"
	batch.ExpectedVersion = 1
	if _, err := stores.Events.AppendBatch(ctx, batch); !errors.Is(err, domain.ErrInvalidEventBatch) {
		t.Fatalf("AppendBatch(duplicate event) error = %v", err)
	}
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM outbox WHERE restaurant_id = $1`, scope.RestaurantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox count after rejected append = %d; want 1", count)
	}
}

func newTestStores(t *testing.T, db *postgres.DB) storecontract.Stores {
	t.Helper()
	ctx := t.Context()
	scope := newTestScope(t, db)
	stores, err := NewRestaurantStores(ctx, db, scope)
	if err != nil {
		t.Fatal(err)
	}
	return storecontract.Stores{Events: stores.Events, Snapshots: stores.Snapshots}
}

func newTestScope(t *testing.T, db *postgres.DB) contract.Scope {
	t.Helper()
	ctx := t.Context()
	generation, err := postgres.Generation(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	organizationID, restaurantID := testUUID(t), testUUID(t)
	scope := contract.Scope{OrganizationID: organizationID, RestaurantID: restaurantID, RuntimeEpoch: generation,
		LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	insertTenant(t, db, scope)
	return scope
}

func insertTenant(t *testing.T, db *postgres.DB, scope contract.Scope) {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	if _, err := db.Pool().Exec(ctx, `INSERT INTO restaurants (restaurant_id, organization_id, lifecycle_version, time_zone, settings_version, state, coverage_from, registered_at)
		VALUES ($1, $2, 1, 'Europe/Moscow', 1, 'active', $3, $3)`, scope.RestaurantID, scope.OrganizationID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO streams (restaurant_id, version, updated_at) VALUES ($1, 0, $2)`, scope.RestaurantID, now); err != nil {
		t.Fatal(err)
	}
}

func toPostgresScope(scope contract.Scope) postgres.Scope {
	return postgres.Scope{Actor: postgres.ActorTenant, OrgID: scope.OrganizationID, RestaurantID: scope.RestaurantID}
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func runtimeURL(rawURL, username, password string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String(), nil
}

func testUUID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
