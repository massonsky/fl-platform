"""VPS runner: disposable DB/bucket and two API containers, no live worker changes."""
import json
from pathlib import Path
import subprocess
import sys
import uuid
import xml.etree.ElementTree as ET

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'infra/vps'))
from check_s3 import request

ROOT = Path(__file__).resolve().parents[2]
IMAGE = 'fl-platform/control-plane:release-test'


def command(args, capture=False):
    return subprocess.run(args, check=True, stdin=subprocess.DEVNULL,
                          capture_output=capture, text=True)


def check_bootstrap(pg, database, bootstrap):
    """Exercise fresh/repeated bootstrap and fail-closed schema checks in this disposable DB."""
    if not database.startswith('flrp_check_'):
        raise ValueError('Bootstrap acceptance requires a disposable database')
    psql = pg + ['psql', '-U', 'flrp', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At', '-c']

    def query(sql):
        return command(psql + [sql], True).stdout.strip()

    def reject(expected_error):
        result = subprocess.run(bootstrap, stdin=subprocess.DEVNULL,
                                capture_output=True, text=True)
        assert result.returncode != 0, 'Bootstrap accepted an untracked/unknown schema'
        assert expected_error in result.stderr, 'Bootstrap did not report the schema mismatch'

    # A pre-existing untracked table must not be silently marked as migrated.
    query('CREATE TABLE users (id integer); INSERT INTO users VALUES (42)')
    reject('untracked database schema')
    assert query('SELECT id FROM users') == '42'
    assert query("SELECT to_regclass('public.schema_migrations') IS NULL") == 't'
    query('DROP TABLE users')

    command(bootstrap)
    expected = [path.name.removesuffix('.up.sql')
                for path in sorted((ROOT / 'db/migrations').glob('*.up.sql'))]
    actual = query('SELECT version FROM schema_migrations ORDER BY version').splitlines()
    assert actual == expected, f'Incomplete bootstrap ledger: {actual}'
    for table in ['ai_providers', 'ai_conversations', 'ai_generations', 'ai_assistants']:
        assert query(f"SELECT to_regclass('public.{table}') IS NOT NULL") == 't', table
    snapshot = query("SELECT json_build_array((SELECT count(*) FROM users WHERE external_subject='admin'),"
                     '(SELECT count(*) FROM model_presets),(SELECT count(*) FROM artifacts))')
    counts = json.loads(snapshot)
    assert counts[0] == 1 and counts[1] > 0 and counts[2] > 0, counts
    command(bootstrap)
    assert query("SELECT json_build_array((SELECT count(*) FROM users WHERE external_subject='admin'),"
                 '(SELECT count(*) FROM model_presets),(SELECT count(*) FROM artifacts))') == snapshot
    assert query('SELECT version FROM schema_migrations ORDER BY version').splitlines() == expected

    query("INSERT INTO schema_migrations(version) VALUES('999999_unknown_release')")
    reject('migration ledger is not a prefix')
    assert query("SELECT count(*) FROM schema_migrations WHERE version='999999_unknown_release'") == '1'
    query("DELETE FROM schema_migrations WHERE version='999999_unknown_release'")
    print('PASS: all release migrations, idempotent bootstrap, untracked/unknown schema rejection')


def main():
    suffix = uuid.uuid4().hex[:12]
    database = 'flrp_check_' + suffix
    bucket = 'flrp-check-' + suffix
    containers = [bucket + '-a', bucket + '-b', bucket + '-coordinator']
    pg = ['docker', 'compose', 'exec', '-T', 'postgres']
    common = ['docker', 'run', '--rm', '--network', 'fl-platform_default',
              '--memory', '256m', '--cpus', '0.5',
              '-e', 'FL_PG_DATABASE=' + database, '-e', 'FL_S3_BUCKET=' + bucket,
              '-v', '/opt/fl-platform/secrets:/run/secrets:ro']
    command(pg + ['createdb', '-U', 'flrp', database])
    created_bucket = False
    try:
        assert request('PUT', '/' + bucket)[0] == 200
        created_bucket = True
        bootstrap = common + ['-v', '/opt/fl-platform/presets:/presets:ro',
            '-v', str(ROOT / 'db/migrations') + ':/migrations:ro',
            '--entrypoint', '/usr/local/bin/bootstrap', IMAGE]
        check_bootstrap(pg, database, bootstrap)
        for migration in ['000003_administration.down.sql', '000003_administration.up.sql']:
            with (ROOT / 'db/migrations' / migration).open('rb') as source:
                subprocess.run(pg + ['psql', '-U', 'flrp', '-d', database,
                    '-v', 'ON_ERROR_STOP=1'], stdin=source, check=True, stdout=subprocess.DEVNULL)
        print('PASS: 000003 up/down/up in disposable database')
        for index, name in enumerate(containers):
            binary = 'api' if index < 2 else 'orchestrator'
            command(common + ['-d', '--name', name, '--entrypoint', '/usr/local/bin/' + binary, IMAGE], True)
        client_command = ['docker', 'run', '--rm', '--network', 'fl-platform_default',
            '--memory', '450m', '-v', str(ROOT) + ':/src:ro',
            '-v', '/opt/fl-platform/secrets:/run/secrets:ro',
            '-e', 'TEST_API_A=' + containers[0], '-e', 'TEST_API_B=' + containers[1],
            '-e', 'PYTHONPATH=/src/services/ml-client/src:/src/services/ml-client/src/fl_client/gen:/src/tests/system',
            '--entrypoint', 'python', 'fl-platform/ml-client:mvp', '/src/tests/integration/two_api.py']
        command(client_command)
        command(client_command[:-1] + ['/src/tests/integration/administration.py'])
        sql = """SELECT json_build_object('updates',(SELECT count(*) FROM updates WHERE status='accepted'),
          'trained_tasks',(SELECT count(*) FROM tasks WHERE kind='train'),
          'models',(SELECT count(*) FROM model_versions),
          'eval_tasks',(SELECT count(*) FROM tasks WHERE kind='evaluate'))"""
        result = command(pg + ['psql', '-U', 'flrp', '-d', database, '-At', '-c', sql], True)
        counts = json.loads(result.stdout)
        assert counts == {'updates': 5, 'trained_tasks': 5, 'models': 2, 'eval_tasks': 1}, counts
        print('PASS: SQL exact counts', counts)
    finally:
        for name in containers:
            subprocess.run(['docker', 'rm', '-f', name], stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        command(pg + ['dropdb', '-U', 'flrp', '--if-exists', database])
        if created_bucket:
            status, body = request('GET', '/' + bucket)
            assert status == 200
            listing = ET.fromstring(body)
            ns = {'s': 'http://s3.amazonaws.com/doc/2006-03-01/'}
            assert listing.findtext('s:IsTruncated', namespaces=ns) == 'false'
            for key in listing.findall('s:Contents/s:Key', ns):
                assert request('DELETE', '/' + bucket + '/' + key.text)[0] == 204
            assert request('DELETE', '/' + bucket)[0] == 204
        print('PASS: disposable containers, database and bucket removed')


if __name__ == '__main__':
    main()
