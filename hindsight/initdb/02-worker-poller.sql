CREATE OR REPLACE FUNCTION public.schemas_with_pending_work()
RETURNS SETOF text AS $$
DECLARE
  r RECORD;
  has_work BOOLEAN;
BEGIN
  BEGIN
    SELECT EXISTS(
      SELECT 1 FROM public.async_operations
      WHERE status = 'pending'
        AND task_payload IS NOT NULL
      LIMIT 1
    ) INTO has_work;

    IF has_work THEN
      RETURN NEXT NULL;
    END IF;
  EXCEPTION WHEN OTHERS THEN
    NULL;
  END;

  FOR r IN
    SELECT nspname
    FROM pg_namespace
    WHERE nspname LIKE 'tenant_%'
  LOOP
    BEGIN
      EXECUTE format(
        'SELECT EXISTS(SELECT 1 FROM %I.async_operations WHERE status = ''pending'' AND task_payload IS NOT NULL LIMIT 1)',
        r.nspname
      ) INTO has_work;

      IF has_work THEN
        RETURN NEXT r.nspname;
      END IF;
    EXCEPTION WHEN OTHERS THEN
      NULL;
    END;
  END LOOP;
END
$$ LANGUAGE plpgsql STABLE;
