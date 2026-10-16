DO $warehouse_snapshot_privilege$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umir_warehouse') THEN
        GRANT DELETE ON snapshots TO umir_warehouse;
    END IF;
END;
$warehouse_snapshot_privilege$;
