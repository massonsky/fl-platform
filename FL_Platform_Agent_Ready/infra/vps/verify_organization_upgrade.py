"""REL-06: read-only live backup; schema6 -> 8 rehearsal in a fresh container."""
import hashlib
import json
import os
import re
import subprocess
import uuid
from pathlib import Path

from check_migration_ledger import command, dump, normalize_dump, release_migrations, sql_command
from verify_ledger_repair import isolated_container, ledger_rows, sql, versions

ROOT = Path(__file__).resolve().parents[2]
LIVE = 'fl-platform-postgres-1'
BASE_SCHEMA = '57a401a89f7a45d83ab19b541557843e37bdb7f3c259e4950fddda372aacc860'
NEW_PROJECT_FIELDS = ['organization_id', 'created_by', 'access_mode', 'visibility', 'acl_revision']


def disposable(container, live_id):
    if not re.fullmatch(r'[0-9a-f]{64}', container) or container == live_id:
        raise ValueError('Mutation target is not a disposable container')


def migration_sql(migrations, applied):
    names = [name for name, _ in migrations]
    if applied != names[:len(applied)] or len(applied) > len(names):
        raise ValueError('Unknown or non-prefix ledger')
    statements = ["BEGIN; SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='60s';",
                  'SELECT pg_advisory_xact_lock(726182901);',
                  'CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());']
    for name, source in migrations[len(applied):]:
        if not re.fullmatch(r'[0-9]{6}_[a-z][a-z0-9_]*', name):
            raise ValueError('Invalid migration name')
        statements.extend([source, "INSERT INTO schema_migrations(version) VALUES ('" + name + "');"])
    return '\n'.join(statements + ['COMMIT;'])


def apply(container, database, migrations, applied, live_id):
    disposable(container, live_id)
    command(sql_command(container, database, 'flrp'), migration_sql(migrations, applied), timeout=180)


def signature(container, tables):
    for name in tables:
        if not re.fullmatch(r'[a-z][a-z0-9_]*', name):
            raise ValueError('Invalid table identifier')
    args = ['docker', 'exec', container, 'pg_dump', '-U', 'flrp', '-d', 'flrp',
            '--data-only', '--inserts', '--no-owner', '--no-privileges']
    raw = command(args + ['--table=public.' + name for name in tables], timeout=180).stdout
    projects = sql(container, 'flrp', "SELECT coalesce(jsonb_agg(to_jsonb(p)-ARRAY[" +
        ','.join("'" + name + "'" for name in NEW_PROJECT_FIELDS) +
        "] ORDER BY id),'[]'::jsonb) FROM projects p")
    return hashlib.sha256((normalize_dump(raw) + projects).encode()).hexdigest()


def backfill_checks(container):
    failures = sql(container, 'flrp', """SELECT
      (SELECT count(*) FROM projects p LEFT JOIN organizations o ON o.id=p.organization_id
        LEFT JOIN organization_memberships m ON m.organization_id=o.id AND m.user_id=p.owner_id
        JOIN users u ON u.id=p.owner_id
        WHERE o.id IS NULL OR o.kind<>'personal' OR o.personal_owner_id IS DISTINCT FROM p.owner_id
          OR p.created_by IS DISTINCT FROM p.owner_id OR p.access_mode<>'legacy_private' OR p.visibility<>'private'
          OR p.acl_revision<>1 OR m.user_id IS NULL OR m.role<>'owner'
          OR m.status<>CASE WHEN u.disabled_at IS NULL THEN 'active' ELSE 'suspended' END)
      +(SELECT count(*) FROM project_members)
      +(SELECT count(*) FROM project_access_events)
      +(SELECT count(*) FROM organizations WHERE kind<>'personal')""")
    if failures != '0':
        raise ValueError('Legacy ownership/membership backfill mismatch')


def main():
    os.umask(0o077)
    migrations = release_migrations(ROOT / 'db/migrations', 8)
    expected = [name for name, _ in migrations]
    live_id = command(['docker', 'inspect', '-f', '{{.Id}}', LIVE]).stdout.strip()
    if not re.fullmatch(r'[0-9a-f]{64}', live_id):
        raise ValueError('Invalid live identity')
    live_schema = normalize_dump(dump(LIVE, 'flrp', 'flrp'))
    live_ledger = ledger_rows(LIVE, 'flrp')
    if hashlib.sha256(live_schema.encode()).hexdigest() != BASE_SCHEMA or versions(LIVE, 'flrp') != expected[:6]:
        raise ValueError('Live schema6 baseline changed')
    backup = Path('/opt/fl-platform/backups') / ('pre-organizations-' + uuid.uuid4().hex)
    backup.mkdir(mode=0o700)
    with (backup / 'postgres.dump').open('xb') as output:
        subprocess.run(['docker', 'exec', LIVE, 'pg_dump', '-U', 'flrp', '-Fc', 'flrp'],
            stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.PIPE, check=True, timeout=180)
    with isolated_container() as target:
        disposable(target, live_id)
        with (backup / 'postgres.dump').open('rb') as source:
            subprocess.run(['docker', 'exec', '-i', target, 'pg_restore', '-U', 'flrp', '-d', 'flrp',
                '--exit-on-error', '--no-owner'], stdin=source, stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE, check=True, timeout=180)
        if normalize_dump(dump(target, 'flrp', 'flrp')) != live_schema or ledger_rows(target, 'flrp') != live_ledger:
            raise ValueError('Restored baseline mismatch')
        tables = json.loads(sql(target, 'flrp', """SELECT json_agg(tablename ORDER BY tablename)
          FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('projects','schema_migrations')"""))
        before = signature(target, tables)
        apply(target, 'flrp', migrations, expected[:6], live_id)
        if versions(target, 'flrp') != expected or signature(target, tables) != before:
            raise ValueError('Upgrade changed legacy application data')
        backfill_checks(target)
        # The nullable legacy column must not let SQL's UNKNOWN hide a bad backfill.
        if sql(target, 'flrp', 'SELECT count(*) FROM projects') == '0':
            raise ValueError('Rehearsal requires an existing legacy project')
        sql(target, 'flrp', 'UPDATE projects SET created_by=NULL WHERE id=(SELECT id FROM projects ORDER BY id LIMIT 1)')
        try:
            backfill_checks(target)
        except ValueError:
            pass
        else:
            raise ValueError('NULL creator corruption was not detected')
        sql(target, 'flrp', 'UPDATE projects SET created_by=owner_id WHERE created_by IS NULL')
        backfill_checks(target)
        schema8 = normalize_dump(dump(target, 'flrp', 'flrp'))
        command(['docker', 'exec', target, 'createdb', '-U', 'flrp', 'clean_reference'])
        apply(target, 'clean_reference', migrations, [], live_id)
        if normalize_dump(dump(target, 'clean_reference', 'flrp')) != schema8:
            raise ValueError('Upgrade schema differs from clean installation')
        ledger8 = ledger_rows(target, 'flrp')
        all_tables = tables + ['organizations', 'organization_memberships', 'organization_events', 'project_members', 'project_access_events']
        upgraded = signature(target, all_tables)
        apply(target, 'flrp', migrations, expected, live_id)
        if ledger_rows(target, 'flrp') != ledger8 or signature(target, all_tables) != upgraded:
            raise ValueError('Repeat changed data or migration timestamps')
        backfill_checks(target)
    # Context cleanup has succeeded before any PASS proof is written.
    if normalize_dump(dump(LIVE, 'flrp', 'flrp')) != live_schema or ledger_rows(LIVE, 'flrp') != live_ledger:
        raise ValueError('Live schema/ledger changed during rehearsal')
    proof = dict(status='passed', backup=str(backup), restore=True, upgraded_to=expected[-1],
                 clean_schema_match=True, repeat_unchanged=True, legacy_data_unchanged=True,
                 backfill_checked=True, null_backfill_rejected=True, cleanup=True, live_migration_performed=False,
                 legacy_data_sha256=before, schema8_sha256=hashlib.sha256(schema8.encode()).hexdigest())
    (backup / 'upgrade-proof.json').write_text(json.dumps(proof, indent=2) + '\n')
    print(json.dumps(proof, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'failed', 'error_type': type(error).__name__}))
        raise SystemExit(1)
