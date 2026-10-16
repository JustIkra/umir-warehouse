package warehousepg

import (
	"context"
	"embed"

	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
)

//go:embed migrations/*.sql
var warehouseMigrationFiles embed.FS

var warehouseMigrations = postgres.Spec{Service: "warehouse", FS: warehouseMigrationFiles, Dir: "migrations"}

func Migrate(ctx context.Context, db *postgres.DB) error {
	return postgres.Migrate(ctx, db, warehouseMigrations)
}

func VerifySchema(ctx context.Context, db *postgres.DB) error {
	return postgres.VerifySchema(ctx, db, warehouseMigrations)
}
