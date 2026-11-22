"""Back up live SQL; test ledger repair only in a fresh isolated test container.

Never executes reconciliation against the live database. Does not print SQL data.
"""
import hashlib
import json
import os
from contextlib import contextmanager
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid

from check_migration_ledger import command, dump, normalize_dump, sql_command

ROOT = Path(__file__).resolve().parents[2]
LIVE = 'fl-platform-postgres-1'
OLD = ['000001_init', '000002_runtime', '000003_administration']
EXPECTED = OLD + ['000004_ai_providers', '000005_ai_chat', '000006_ai_assistants']
SCHEMA_HASH = '57a401a89f7a45d83ab19b541557843e37bdb7f3c259e4950fddda372aacc860'


@contextmanager
def isolated_container():
    image = command(['docker', 'inspect', '-f', '{{.Image}}', LIVE]).stdout.strip()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
        raise ValueError('Invalid reference image')
    container, network = '', ''
    with tempfile.TemporaryDirectory(prefix='fl-ledger-restore-') as directory:
        password = Path(directory) / 'password'
        password.write_text(secrets.token_hex(32))
        password.chmod(0o600)
        try:
            network = command(['docker', 'network', 'create', '--internal',
                'fl-ledger-restore-' + uuid.uuid4().hex]).stdout.strip()
            if not re.fullmatch(r'[0-9a-f]{64}', network):
                raise ValueError('Invalid created network ID')
            container = command(['docker', 'create', '--pull=never', '--network', network,
                '--memory', '512m', '--cpus', '0.5', '--read-only',
                '--tmpfs', '/var/lib/postgresql/data:rw,noexec,nosuid,size=512m',
                '--tmpfs', '/var/run/postgresql:rw,noexec,nosuid,size=16m',
                '--mount', 'type=bind,src=' + str(password) + ',dst=/run/secrets/password,readonly',
                '-e', 'POSTGRES_PASSWORD_FILE=/run/secrets/password', '-e', 'POSTGRES_USER=flrp',
                '-e', 'POSTGRES_DB=flrp', image]).stdout.strip()
            if not re.fullmatch(r'[0-9a-f]{64}', container):
                raise ValueError('Invalid created container ID')
            command(['docker', 'start', container])
            for _ in range(60):
                if command(['docker', 'exec', container, 'pg_isready', '-h', '127.0.0.1',
                    '-U', 'flrp', '-d', 'flrp'], check=False, timeout=5).returncode == 0:
                    break
                time.sleep(1)
            else:
                raise TimeoutError('Disposable PostgreSQL startup')
            yield container
        finally:
            failures = []
            for identifier, removal in [(container, ['docker', 'rm', '--force', '--volumes']),
                                        (network, ['docker', 'network', 'rm'])]:
                if re.fullmatch(r'[0-9a-f]{64}', identifier):
                    try:
                        if command(removal + [identifier], check=False).returncode:
                            failures.append(identifier)
                    except (OSError, subprocess.SubprocessError):
                        failures.append(identifier)
            if failures:
                raise ValueError('Disposable resource cleanup failed')


def sql(container, database, statement):
    return command(sql_command(container, database, 'flrp', statement)).stdout.strip()


def versions(container, database):
    return json.loads(sql(container, database,
        "SELECT coalesce(json_agg(version ORDER BY version),'[]'::json) FROM schema_migrations"))


def ledger_rows(container, database):
    return sql(container, database, "SELECT coalesce(json_agg(row_to_json(m) ORDER BY version),'[]'::json) FROM schema_migrations m")


def data_hash(container, database):
    raw = command(['docker', 'exec', container, 'pg_dump', '-U', 'flrp', '-d', database,
        '--data-only', '--inserts', '--no-owner', '--no-privileges',
        '--exclude-table-data=public.schema_migrations'], timeout=120).stdout
    return hashlib.sha256(normalize_dump(raw).encode()).hexdigest()


def rehearse(test, backup, live_schema):
    database = 'ledger_restore_' + uuid.uuid4().hex
    command(['docker', 'exec', test, 'createdb', '-U', 'flrp', database])
    proof = {'backup': str(backup), 'live_ledger_changed': False}
    try:
        with (backup / 'postgres.dump').open('rb') as source:
            subprocess.run(['docker', 'exec', '-i', test, 'pg_restore', '-U', 'flrp',
                '-d', database, '--exit-on-error', '--no-owner'], stdin=source,
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=True, timeout=120)
        schema = normalize_dump(dump(test, database, 'flrp'))
        if schema != live_schema or versions(test, database) != OLD:
            raise ValueError('Restore does not match verified schema and ledger')
        baseline = data_hash(test, database)
        repair = (ROOT / 'infra/vps/reconcile_ai_ledger.sql').read_text(encoding='utf-8')
        repaired_rows = None
        for _ in range(2):
            command(sql_command(test, database, 'flrp'), repair)
            if versions(test, database) != EXPECTED or data_hash(test, database) != baseline:
                raise ValueError('Repair/repeat changed application data or wrong ledger')
            current_rows = ledger_rows(test, database)
            if repaired_rows is not None and current_rows != repaired_rows:
                raise ValueError('Repeat changed ledger timestamps')
            repaired_rows = current_rows
        sql(test, database, "INSERT INTO schema_migrations(version) VALUES('999999_unknown')")
        before = ledger_rows(test, database)
        rejected = command(sql_command(test, database, 'flrp'), repair, check=False)
        if rejected.returncode == 0 or ledger_rows(test, database) != before:
            raise ValueError('Unknown ledger was not rejected atomically')
        if normalize_dump(dump(test, database, 'flrp')) != schema or data_hash(test, database) != baseline:
            raise ValueError('Application data/schema changed during rehearsal')
        if versions(LIVE, 'flrp') != OLD:
            raise ValueError('Live ledger changed during rehearsal')
        proof.update(status='passed', restore=True, repair=True, repeat=True,
            unknown_version_rejected=True, application_data_unchanged=True,
            schema_sha256=SCHEMA_HASH, application_data_sha256=baseline)
    finally:
        command(['docker', 'exec', test, 'dropdb', '-U', 'flrp', database])
    return proof


def main():
    os.umask(0o077)
    live_schema = normalize_dump(dump(LIVE, 'flrp', 'flrp'))
    if hashlib.sha256(live_schema.encode()).hexdigest() != SCHEMA_HASH or versions(LIVE, 'flrp') != OLD:
        raise ValueError('Live baseline changed; backup rehearsal refused')
    backup = Path('/opt/fl-platform/backups') / ('pre-ledger-' + uuid.uuid4().hex)
    backup.mkdir(mode=0o700)
    with (backup / 'postgres.dump').open('xb') as output:
        subprocess.run(['docker', 'exec', LIVE, 'pg_dump', '-U', 'flrp', '-Fc', 'flrp'],
            stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.PIPE, check=True, timeout=120)
    with isolated_container() as test:
        proof = rehearse(test, backup, live_schema)
    (backup / 'restore-proof.json').write_text(json.dumps(proof, indent=2) + '\n')
    print(json.dumps(proof, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'failed', 'error_type': type(error).__name__}))
        raise SystemExit(1)
