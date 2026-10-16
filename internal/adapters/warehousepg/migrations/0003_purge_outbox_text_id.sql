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
    DELETE FROM public.outbox WHERE restaurant_id = target_restaurant::text;
    DELETE FROM public.streams WHERE restaurant_id = target_restaurant;
END;
$purge$;
