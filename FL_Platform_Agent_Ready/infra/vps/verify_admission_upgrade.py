"""REL-07: read-only live schema6 backup; schema9/bootstrap rehearsal on copies."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import uuid

from admission_upgrade_checks import backfill, bootstrap_checks, corruption_check, expected_admissions, image_id
from check_migration_ledger import command, dump, normalize_dump, release_migrations
from verify_ledger_repair import data_hash, isolated_container, ledger_rows, sql, versions
from verify_organization_upgrade import BASE_SCHEMA, LIVE, ROOT, apply, backfill_checks, disposable, signature


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def baseline(container, expected):
    schema = normalize_dump(dump(container, 'flrp', 'flrp'))
    if digest(schema) != BASE_SCHEMA or versions(container, 'flrp') != expected[:6]:
        raise ValueError('Live schema6 baseline changed')
    return schema, ledger_rows(container, 'flrp')


def restore(container, backup, live_id):
    disposable(container, live_id)
    with (backup / 'postgres.dump').open('rb') as source:
        subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'flrp', '-d', 'flrp',
            '--exit-on-error', '--no-owner'], stdin=source, stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE, check=True, timeout=180)


def rehearse(container, backup, migrations, live_id, live_schema, live_ledger, image):
    disposable(container, live_id)
    expected = [name for name, _ in migrations]
    restore(container, backup, live_id)
    if normalize_dump(dump(container, 'flrp', 'flrp')) != live_schema or ledger_rows(container, 'flrp') != live_ledger:
        raise ValueError('Restored baseline mismatch')
    tables = json.loads(sql(container, 'flrp', """SELECT json_agg(tablename ORDER BY tablename)
      FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('projects','schema_migrations')"""))
    before = signature(container, tables)
    admitted = expected_admissions(container)
    apply(container, 'flrp', migrations, expected[:6], live_id)
    if versions(container, 'flrp') != expected or signature(container, tables) != before:
        raise ValueError('Upgrade changed legacy application data')
    backfill_checks(container)
    backfill(container, admitted)
    upgraded = data_hash(container, 'flrp')
    corruption_check(container, admitted, live_id)
    if data_hash(container, 'flrp') != upgraded:
        raise ValueError('Negative admission check did not roll back')
    schema9 = normalize_dump(dump(container, 'flrp', 'flrp'))
    command(['docker', 'exec', container, 'createdb', '-U', 'flrp', 'clean_reference'])
    apply(container, 'clean_reference', migrations, [], live_id)
    if normalize_dump(dump(container, 'clean_reference', 'flrp')) != schema9:
        raise ValueError('Upgrade schema differs from clean1-9')
    ledger9 = ledger_rows(container, 'flrp')
    apply(container, 'flrp', migrations, expected, live_id)
    if (ledger_rows(container, 'flrp') != ledger9 or data_hash(container, 'flrp') != upgraded
            or normalize_dump(dump(container, 'flrp', 'flrp')) != schema9):
        raise ValueError('Repeat changed schema, data or migration timestamps')
    backfill_checks(container)
    backfill(container, admitted)
    bootstrap_checks(container, migrations, image, live_id)
    if signature(container, tables) != before:
        raise ValueError('Bootstrap changed legacy application data')
    if (normalize_dump(dump(container, 'fresh_admission', 'flrp')) != schema9
            or versions(container, 'fresh_admission') != expected):
        raise ValueError('Fresh bootstrap schema/ledger mismatch')
    if normalize_dump(dump(container, 'flrp', 'flrp')) != schema9:
        raise ValueError('Bootstrap changed restored schema')
    return dict(restore=True, upgraded_to=expected[-1], clean_schema_match=True,
        repeat_unchanged=True, legacy_data_unchanged=True, organization_backfill_checked=True,
        admission_backfill_checked=True, admission_count=len(admitted), admission_corruption_rejected=True,
        existing_bootstrap_unchanged=True, existing_revocation_preserved=True,
        fresh_bootstrap_admission=True, fresh_revocation_preserved=True,
        legacy_data_sha256=before, schema9_sha256=digest(schema9))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bootstrap-image', '--api-image', required=True,
                        help='Existing local image tag or immutable ID; inspected before any mutation')
    args = parser.parse_args()
    os.umask(0o077)
    image = image_id(args.bootstrap_image)
    migrations = release_migrations(ROOT / 'db/migrations', 9)
    expected = [name for name, _ in migrations]
    live_id = command(['docker', 'inspect', '-f', '{{.Id}}', LIVE]).stdout.strip()
    if not re.fullmatch(r'[0-9a-f]{64}', live_id):
        raise ValueError('Invalid live identity')
    live_schema, live_ledger = baseline(live_id, expected)
    backup = Path('/opt/fl-platform/backups') / ('pre-admission-' + uuid.uuid4().hex)
    backup.mkdir(mode=0o700)
    with (backup / 'postgres.dump').open('xb') as output:
        subprocess.run(['docker', 'exec', live_id, 'pg_dump', '-U', 'flrp', '-Fc', 'flrp'],
            stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.PIPE, check=True, timeout=180)
    with isolated_container() as container:
        proof = rehearse(container, backup, migrations, live_id, live_schema, live_ledger, image)
    # Both fixture contexts have removed credentials/resources before any PASS proof.
    current_id = command(['docker', 'inspect', '-f', '{{.Id}}', LIVE]).stdout.strip()
    current_schema, current_ledger = baseline(live_id, expected)
    if current_id != live_id or current_schema != live_schema or current_ledger != live_ledger:
        raise ValueError('Live identity/schema/ledger changed during rehearsal')
    sources = ['verify_admission_upgrade.py', 'admission_upgrade_checks.py',
               'verify_organization_upgrade.py', 'verify_ledger_repair.py', 'check_migration_ledger.py']
    proof.update(status='passed', backup=str(backup), bootstrap_image=image,
        live_schema_sha256=digest(live_schema), live_ledger_sha256=digest(live_ledger),
        live_identity_unchanged=True, live_schema_ledger_unchanged=True,
        live_migration_performed=False, real_s3_checked=False, cleanup=True,
        source_sha256={name: hashlib.sha256((Path(__file__).parent / name).read_bytes()).hexdigest() for name in sources},
        migrations_sha256={name: digest(source) for name, source in migrations})
    (backup / 'upgrade-proof.json').write_text(json.dumps(proof, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(proof, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'failed', 'error_type': type(error).__name__}))
        raise SystemExit(1)
