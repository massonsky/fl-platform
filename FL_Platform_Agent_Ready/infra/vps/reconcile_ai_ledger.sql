-- REL-05, one-time metadata repair after REL-04 exact schema match and restore proof.
-- This is NOT a migration and must never be discovered by bootstrap.
-- Required schema: exactly release migrations 000001-000006, SHA-256
-- 57a401a89f7a45d83ab19b541557843e37bdb7f3c259e4950fddda372aacc860
-- from check_migration_ledger.py normalization on the deployed PostgreSQL 17.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '15s';
SELECT pg_advisory_xact_lock(726182901);
LOCK TABLE public.schema_migrations IN ACCESS EXCLUSIVE MODE;
DO $repair$
DECLARE
  observed text[];
  old_versions constant text[] := ARRAY[
    '000001_init', '000002_runtime', '000003_administration'];
  complete_versions constant text[] := ARRAY[
    '000001_init', '000002_runtime', '000003_administration',
    '000004_ai_providers', '000005_ai_chat', '000006_ai_assistants'];
BEGIN
  SELECT coalesce(array_agg(version ORDER BY version), ARRAY[]::text[])
    INTO observed FROM public.schema_migrations;
  IF observed = complete_versions THEN
    RETURN;
  END IF;
  IF observed <> old_versions THEN
    RAISE EXCEPTION 'Ledger changed: repair refused';
  END IF;
  INSERT INTO public.schema_migrations(version) VALUES
    ('000004_ai_providers'), ('000005_ai_chat'), ('000006_ai_assistants');
END
$repair$;
COMMIT;
