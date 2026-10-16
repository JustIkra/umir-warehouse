ALTER TABLE events DROP CONSTRAINT events_event_id_check CASCADE;
ALTER TABLE events ADD CONSTRAINT events_event_id_check
    CHECK (octet_length(event_id) BETWEEN 1 AND 256 AND event_id ~ '^[!-~]+$');
