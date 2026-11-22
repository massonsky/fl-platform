"""REL-07 checks and real bootstrap on guarded disposable PostgreSQL only."""
import json
from pathlib import Path
import re
import secrets
import tempfile

from check_migration_ledger import command, sql_command
from verify_ledger_repair import data_hash, ledger_rows, sql
from verify_organization_upgrade import disposable

SNAPSHOT = """SELECT jsonb_build_object(
 'admissions',coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY user_id)
   FROM legacy_lab_admissions a),'[]'::jsonb),
 'events',coalesce((SELECT jsonb_agg(to_jsonb(e) ORDER BY target_user_id,id)
   FROM legacy_lab_admission_events e),'[]'::jsonb))"""


def expected_admissions(container):
    return json.loads(sql(container, 'flrp', """SELECT coalesce(json_agg(id ORDER BY id),'[]'::json)
      FROM users u WHERE role IN ('admin','researcher') OR EXISTS(
        SELECT 1 FROM projects p WHERE p.owner_id=u.id)"""))


def check_backfill(expected, snapshot):
    admissions, events = snapshot['admissions'], snapshot['events']
    if sorted(row['user_id'] for row in admissions) != sorted(expected):
        raise ValueError('Admission identity set mismatch')
    if sorted(row['target_user_id'] for row in events) != sorted(expected):
        raise ValueError('Admission audit set mismatch')
    after = dict(status='active', revision=1, source='legacy_backfill')
    for row in admissions:
        if any(row[key] != value for key, value in after.items()) or row['updated_by'] is not None:
            raise ValueError('Admission metadata mismatch')
    for row in events:
        if (row['action'] != 'legacy_backfilled' or row['actor_id'] is not None
                or row['before_json'] != {} or row['after_json'] != after
                or row['reason'] != 'Migration 000009: existing researcher/admin or legacy project owner'):
            raise ValueError('Admission audit metadata mismatch')


def backfill(container, expected):
    check_backfill(expected, json.loads(sql(container, 'flrp', SNAPSHOT)))


def corruption_check(container, expected, live_id):
    disposable(container, live_id)
    if not expected:
        raise ValueError('Rehearsal requires an existing admitted identity')
    statement = ("BEGIN; UPDATE legacy_lab_admissions SET revision=revision+1 "
                 "WHERE user_id=(SELECT user_id FROM legacy_lab_admissions ORDER BY user_id LIMIT 1); "
                 + SNAPSHOT + '; ROLLBACK;')
    snapshot = json.loads(command(sql_command(container, 'flrp', 'flrp') + ['-q'], statement).stdout)
    try:
        check_backfill(expected, snapshot)
    except ValueError:
        backfill(container, expected)
        return
    raise ValueError('Corrupt admission revision was not detected')


def image_id(value):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}', value):
        raise ValueError('Invalid local bootstrap image reference')
    if value.startswith('sha256:') and not re.fullmatch(r'sha256:[0-9a-f]{64}', value):
        raise ValueError('Invalid bootstrap image digest')
    actual = command(['docker', 'image', 'inspect', '--format', '{{.Id}}', value]).stdout.strip()
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', actual) or value.startswith('sha256:') and actual != value:
        raise ValueError('Bootstrap image identity mismatch')
    return actual


def bootstrap(container, database, image, directory, live_id):
    disposable(container, live_id)
    if database not in ('flrp', 'fresh_admission') or not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
        raise ValueError('Invalid bootstrap fixture target')
    extra = []
    for source, destination in [('secrets', '/run/secrets'), ('presets', '/presets'), ('migrations', '/migrations')]:
        extra += ['--mount', f'type=bind,source={directory / source},target={destination},readonly']
    created = ''
    try:
        created = command(['docker', 'create', '--pull=never', '--network', 'container:' + container,
            '--memory', '256m', '--cpus', '1', '--read-only', *extra,
            '-e', 'FL_PG_HOST=127.0.0.1:5432', '-e', 'FL_PG_DATABASE=' + database,
            '-e', 'FL_SECRETS_DIR=/run/secrets', '-e', 'FL_S3_ENDPOINT=127.0.0.1:1',
            '-e', 'FL_S3_BUCKET=rehearsal-unused', '--entrypoint', '/usr/local/bin/bootstrap', image]).stdout.strip()
        disposable(created, live_id)
        command(['docker', 'start', '--attach', created], timeout=120)
        state = json.loads(command(['docker', 'inspect', '--format', '{{json .State}}', created]).stdout)
        if state.get('Status') != 'exited' or state.get('ExitCode') != 0:
            raise ValueError('Disposable bootstrap did not succeed')
    finally:
        if re.fullmatch(r'[0-9a-f]{64}', created) and created != live_id:
            command(['docker', 'rm', '--force', '--volumes', created])


def revoke_admin(container, database, live_id):
    disposable(container, live_id)
    statement = """BEGIN;
      WITH changed AS (UPDATE legacy_lab_admissions SET status='revoked',revision=revision+1,
        source='admin',updated_by=user_id,updated_at=clock_timestamp()
        WHERE user_id=(SELECT id FROM users WHERE external_subject='admin') AND status='active'
        RETURNING user_id,status,revision,source)
      INSERT INTO legacy_lab_admission_events(target_user_id,actor_id,action,before_json,after_json,reason)
      SELECT user_id,user_id,'changed','{"status":"active","revision":1}',
        jsonb_build_object('status',status,'revision',revision,'source',source),
        'REL-07 disposable rehearsal only' FROM changed;
      COMMIT;"""
    command(sql_command(container, database, 'flrp'), statement)
    if sql(container, database, """SELECT count(*) FROM legacy_lab_admissions a JOIN users u ON u.id=a.user_id
      WHERE u.external_subject='admin' AND a.status='revoked' AND a.revision=2""") != '1':
        raise ValueError('Disposable admin revoke failed')


def bootstrap_checks(container, migrations, image, live_id):
    disposable(container, live_id)
    with tempfile.TemporaryDirectory(prefix='fl-admission-bootstrap-') as name:
        directory = Path(name)
        directory.chmod(0o700)
        for child in ('secrets', 'presets', 'migrations'):
            (directory / child).mkdir(mode=0o700)
        password = secrets.token_hex(32)
        for key in ('postgres_password', 'admin_password', 'minio_user', 'minio_password'):
            path = directory / 'secrets' / key
            path.write_text(password if key == 'postgres_password' else secrets.token_hex(32))
            path.chmod(0o600)
        # This role belongs only to the new PostgreSQL container; never pass its secret in argv.
        command(sql_command(container, 'flrp', 'flrp'), "ALTER ROLE flrp PASSWORD '" + password + "';")
        (directory / 'presets/presets.json').write_text('[]')
        for version, source in migrations:
            (directory / 'migrations' / (version + '.up.sql')).write_text(source, encoding='utf-8')
        if sql(container, 'flrp', "SELECT count(*) FROM users WHERE external_subject='admin'") != '1':
            raise ValueError('Restored database must contain its existing admin')
        baseline, ledger = data_hash(container, 'flrp'), ledger_rows(container, 'flrp')
        bootstrap(container, 'flrp', image, directory, live_id)
        if data_hash(container, 'flrp') != baseline or ledger_rows(container, 'flrp') != ledger:
            raise ValueError('Existing admin bootstrap changed restored data')
        revoke_admin(container, 'flrp', live_id)
        revoked = data_hash(container, 'flrp')
        bootstrap(container, 'flrp', image, directory, live_id)
        if data_hash(container, 'flrp') != revoked or ledger_rows(container, 'flrp') != ledger:
            raise ValueError('Existing admission revocation was not preserved')
        command(['docker', 'exec', container, 'createdb', '-U', 'flrp', 'fresh_admission'])
        bootstrap(container, 'fresh_admission', image, directory, live_id)
        counts = sql(container, 'fresh_admission', """SELECT
          (SELECT count(*) FROM users)::text||'/'||(SELECT count(*) FROM legacy_lab_admissions)::text
          ||'/'||(SELECT count(*) FROM legacy_lab_admission_events)::text||'/'||
          (SELECT count(*) FROM legacy_lab_admissions a JOIN users u ON u.id=a.user_id
            WHERE u.external_subject='admin' AND u.role='admin' AND u.disabled_at IS NULL
              AND a.status='active' AND a.revision=1 AND a.source='bootstrap' AND a.updated_by=u.id)::text||'/'||
          (SELECT count(*) FROM legacy_lab_admission_events WHERE action='bootstrap_admitted'
            AND actor_id=target_user_id AND before_json='{}'::jsonb
            AND after_json='{"status":"active","revision":1,"source":"bootstrap"}'::jsonb)::text""")
        if counts != '1/1/1/1/1':
            raise ValueError('Fresh bootstrap admission/audit mismatch')
        fresh_ledger = ledger_rows(container, 'fresh_admission')
        revoke_admin(container, 'fresh_admission', live_id)
        fresh_revoked = data_hash(container, 'fresh_admission')
        bootstrap(container, 'fresh_admission', image, directory, live_id)
        if data_hash(container, 'fresh_admission') != fresh_revoked or ledger_rows(container, 'fresh_admission') != fresh_ledger:
            raise ValueError('Fresh admission revocation was not preserved')
    if directory.exists():
        raise ValueError('Temporary bootstrap credentials not removed')
