ALTER TABLE events ADD CONSTRAINT events_payload_index_match CHECK (
    (
        payload ?& ARRAY['sequence', 'eventID', 'type', 'schemaVersion', 'commandID', 'recordedAt']
        AND (payload->>'sequence')::bigint = sequence
        AND payload->>'eventID' = event_id
        AND payload->>'type' = event_type
        AND (payload->>'schemaVersion')::smallint = schema_version
        AND payload->>'commandID' = command_id
        AND regexp_replace(payload->>'recordedAt', '(\.[0-9]{6})[0-9]*([+-][0-9]{2}:[0-9]{2}|Z)$', E'\\1\\2')::timestamptz = recorded_at
    ) IS TRUE
);
