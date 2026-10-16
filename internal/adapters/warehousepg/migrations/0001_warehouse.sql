-- umir:include storage_meta

CREATE TABLE generation_history (
    generation uuid PRIMARY KEY,
    previous uuid,
    rotated_at timestamptz NOT NULL,
    reason text NOT NULL
);

CREATE TABLE restaurants (
    restaurant_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    lifecycle_version bigint NOT NULL CHECK (lifecycle_version > 0),
    time_zone text NOT NULL,
    settings_version bigint NOT NULL CHECK (settings_version > 0),
    state text NOT NULL CHECK (state IN ('active', 'closed')),
    register_operation_id uuid,
    close_operation_id uuid,
    coverage_from timestamptz NOT NULL,
    registered_at timestamptz NOT NULL,
    closed_at timestamptz,
    CHECK ((state = 'active' AND closed_at IS NULL) OR (state = 'closed' AND closed_at IS NOT NULL))
);
CREATE INDEX restaurants_by_organization ON restaurants (organization_id);
CREATE INDEX restaurants_closed_at ON restaurants (closed_at) WHERE state = 'closed';

CREATE TABLE streams (
    restaurant_id uuid PRIMARY KEY REFERENCES restaurants (restaurant_id),
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    next_due_at timestamptz,
    updated_at timestamptz NOT NULL
);
CREATE INDEX streams_next_due_at ON streams (next_due_at, restaurant_id) WHERE next_due_at IS NOT NULL;

CREATE TABLE commands (
    restaurant_id uuid NOT NULL,
    command_id text NOT NULL CHECK (char_length(command_id) BETWEEN 1 AND 256),
    command_type text NOT NULL,
    fingerprint text NOT NULL,
    first_sequence bigint NOT NULL CHECK (first_sequence > 0),
    final_sequence bigint NOT NULL CHECK (final_sequence >= first_sequence),
    result jsonb NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, command_id),
    UNIQUE (restaurant_id, final_sequence)
) PARTITION BY HASH (restaurant_id);

CREATE TABLE events (
    restaurant_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    event_id text NOT NULL CHECK (event_id ~ '^[!-~]{1,256}$'),
    command_id text NOT NULL,
    event_type text NOT NULL,
    schema_version smallint NOT NULL CHECK (schema_version IN (1, 2)),
    recorded_at timestamptz NOT NULL,
    proposal_id text,
    order_id text,
    delivery_id text,
    payload jsonb NOT NULL,
    PRIMARY KEY (restaurant_id, sequence),
    UNIQUE (restaurant_id, event_id),
    FOREIGN KEY (restaurant_id, command_id) REFERENCES commands (restaurant_id, command_id)
) PARTITION BY HASH (restaurant_id);
CREATE INDEX events_by_recorded_at ON events (restaurant_id, recorded_at, sequence);

CREATE TABLE snapshots (
    restaurant_id uuid NOT NULL REFERENCES restaurants (restaurant_id),
    last_sequence bigint NOT NULL CHECK (last_sequence >= 0),
    schema_version smallint NOT NULL,
    state jsonb NOT NULL,
    state_sha256 bytea NOT NULL CHECK (octet_length(state_sha256) = 32),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, last_sequence)
);

CREATE TABLE order_attempts (
    restaurant_id uuid NOT NULL REFERENCES restaurants (restaurant_id),
    command_id text NOT NULL CHECK (char_length(command_id) BETWEEN 1 AND 256),
    proposal_id text NOT NULL,
    request_digest bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('executing', 'interrupted', 'rejected', 'recheck', 'completed')),
    executor_instance text,
    lease_until timestamptz,
    result jsonb,
    failure_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (restaurant_id, command_id)
);
CREATE INDEX order_attempts_terminal_retention ON order_attempts (updated_at)
    WHERE status IN ('rejected', 'recheck', 'interrupted');

CREATE TABLE commands_p00 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 0);
CREATE TABLE commands_p01 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 1);
CREATE TABLE commands_p02 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 2);
CREATE TABLE commands_p03 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 3);
CREATE TABLE commands_p04 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 4);
CREATE TABLE commands_p05 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 5);
CREATE TABLE commands_p06 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 6);
CREATE TABLE commands_p07 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 7);
CREATE TABLE commands_p08 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 8);
CREATE TABLE commands_p09 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 9);
CREATE TABLE commands_p10 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 10);
CREATE TABLE commands_p11 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 11);
CREATE TABLE commands_p12 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 12);
CREATE TABLE commands_p13 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 13);
CREATE TABLE commands_p14 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 14);
CREATE TABLE commands_p15 PARTITION OF commands FOR VALUES WITH (MODULUS 16, REMAINDER 15);
CREATE TABLE events_p00 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 0);
CREATE TABLE events_p01 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 1);
CREATE TABLE events_p02 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 2);
CREATE TABLE events_p03 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 3);
CREATE TABLE events_p04 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 4);
CREATE TABLE events_p05 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 5);
CREATE TABLE events_p06 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 6);
CREATE TABLE events_p07 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 7);
CREATE TABLE events_p08 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 8);
CREATE TABLE events_p09 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 9);
CREATE TABLE events_p10 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 10);
CREATE TABLE events_p11 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 11);
CREATE TABLE events_p12 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 12);
CREATE TABLE events_p13 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 13);
CREATE TABLE events_p14 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 14);
CREATE TABLE events_p15 PARTITION OF events FOR VALUES WITH (MODULUS 16, REMAINDER 15);

CREATE UNIQUE INDEX events_proposal_unique ON events (restaurant_id, proposal_id) WHERE proposal_id IS NOT NULL;
CREATE UNIQUE INDEX events_order_unique ON events (restaurant_id, order_id) WHERE order_id IS NOT NULL;
CREATE UNIQUE INDEX events_delivery_unique ON events (restaurant_id, delivery_id) WHERE delivery_id IS NOT NULL;

-- umir:include worker_leases
-- umir:include outbox

DO $warehouse_rls$
DECLARE
    table_name text;
BEGIN
    FOR table_name IN
        SELECT c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = current_schema()
           AND c.relname = ANY (ARRAY[
               'restaurants', 'streams', 'commands', 'events', 'snapshots', 'order_attempts', 'outbox', 'worker_leases'
           ])
           AND c.relkind IN ('r', 'p')
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        IF table_name = 'restaurants' THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I USING (current_setting(''umir.actor'', true) = ''system'' OR (restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid AND organization_id = NULLIF(current_setting(''umir.org_id'', true), '''')::uuid)) WITH CHECK (current_setting(''umir.actor'', true) = ''system'' OR (restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid AND organization_id = NULLIF(current_setting(''umir.org_id'', true), '''')::uuid))',
                table_name || '_scope', table_name);
        ELSIF table_name = 'worker_leases' THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I USING (current_setting(''umir.actor'', true) = ''system'') WITH CHECK (current_setting(''umir.actor'', true) = ''system'')',
                table_name || '_system', table_name);
        ELSIF table_name = 'outbox' THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I USING (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')) WITH CHECK (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), ''''))',
                table_name || '_scope', table_name);
        ELSE
            EXECUTE format(
                'CREATE POLICY %I ON %I USING (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid) WITH CHECK (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid)',
                table_name || '_scope', table_name);
        END IF;
    END LOOP;

    FOR table_name IN
        SELECT child.relname
          FROM pg_inherits i
          JOIN pg_class parent ON parent.oid = i.inhparent
          JOIN pg_class child ON child.oid = i.inhrelid
         WHERE parent.relname IN ('commands', 'events')
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I USING (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid) WITH CHECK (current_setting(''umir.actor'', true) = ''system'' OR restaurant_id = NULLIF(current_setting(''umir.restaurant_id'', true), '''')::uuid)',
            table_name || '_scope', table_name);
    END LOOP;
END;
$warehouse_rls$;

CREATE OR REPLACE FUNCTION purge_restaurant(target_restaurant uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $purge$
DECLARE
    cutoff timestamptz := clock_timestamp() - interval '7 days';
BEGIN
    PERFORM set_config('umir.actor', 'system', true);
    IF NOT EXISTS (
        SELECT 1 FROM public.restaurants
         WHERE restaurant_id = target_restaurant AND state = 'closed' AND closed_at <= cutoff
    ) THEN
        RAISE EXCEPTION 'restaurant is not eligible for retention purge' USING ERRCODE = '22023';
    END IF;
    DELETE FROM public.order_attempts WHERE restaurant_id = target_restaurant;
    DELETE FROM public.snapshots WHERE restaurant_id = target_restaurant;
    DELETE FROM public.events WHERE restaurant_id = target_restaurant;
    DELETE FROM public.commands WHERE restaurant_id = target_restaurant;
    DELETE FROM public.outbox WHERE restaurant_id = target_restaurant;
    DELETE FROM public.streams WHERE restaurant_id = target_restaurant;
END;
$purge$;

REVOKE ALL ON FUNCTION purge_restaurant(uuid) FROM PUBLIC;

DO $warehouse_grants$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umir_warehouse') THEN
        GRANT USAGE ON SCHEMA public TO umir_warehouse;
        GRANT SELECT ON storage_meta, schema_migrations, restaurants, streams, commands, events, snapshots, order_attempts TO umir_warehouse;
        GRANT INSERT, UPDATE ON restaurants, streams, snapshots, order_attempts TO umir_warehouse;
        GRANT INSERT ON commands, events TO umir_warehouse;
        GRANT SELECT, INSERT, UPDATE, DELETE ON outbox, worker_leases TO umir_warehouse;
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO umir_warehouse;
        GRANT EXECUTE ON FUNCTION purge_restaurant(uuid) TO umir_warehouse;
    END IF;
END;
$warehouse_grants$;
