//go:build integration

package warehousepg

import (
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/tests/itest"
)

func TestWarehouseMigrationsCreateGenerationAndForcedTenantPolicies(t *testing.T) {
	ctx := t.Context()
	db, err := postgres.Open(ctx, postgres.Config{URL: itest.NewDatabase(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	first, err := postgres.Generation(ctx, db)
	if err != nil || first == "" {
		t.Fatalf("generation = %q, err = %v", first, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}
	if err := VerifySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	second, err := postgres.Generation(ctx, db)
	if err != nil || second != first {
		t.Fatalf("generation after repeat = %q, err = %v; want %q", second, err, first)
	}

	var count int
	if err := db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM pg_class
		WHERE relname IN ('restaurants', 'streams', 'commands', 'events', 'snapshots', 'order_attempts', 'outbox', 'worker_leases')
		  AND relrowsecurity AND relforcerowsecurity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("forced RLS tables = %d, want 8", count)
	}
	if err := db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM pg_class
		WHERE relname ~ '^(commands|events)_p[0-9]{2}$'
		  AND relrowsecurity AND relforcerowsecurity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 32 {
		t.Fatalf("forced RLS partitions = %d, want 32", count)
	}
}
