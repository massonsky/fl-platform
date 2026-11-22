"""COM-02C3D: real browser/API acceptance in disposable containers; no live fallback."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import uuid

from run_release_checks import ROOT, command, postgres_image, remove_resources, wait_postgres


def options():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-image', default=os.environ.get('FL_QA_API_IMAGE', os.environ.get('API_IMAGE', 'fl-platform/control-plane:mvp')))
    parser.add_argument('--web-image', default=os.environ.get('FL_QA_WEB_IMAGE', os.environ.get('WEB_IMAGE', 'fl-platform/web:mvp')))
    parser.add_argument('--browser-image', default=os.environ.get('FL_QA_BROWSER_IMAGE', 'fl-platform/browser-check:mvp'))
    parser.add_argument('--minio-image', default=os.environ.get('FL_QA_MINIO_IMAGE', 'fl-platform/minio:RELEASE.2025-10-15T17-29-55Z'))
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--browser-check', choices=('organization-projects-real', 'legacy-lab-real'),
                        default='organization-projects-real')
    result = parser.parse_args()
    return result


def main():
    args = options()
    if not shutil.which('openssl'):
        raise RuntimeError('Existing openssl is required; runner never installs dependencies')
    tags = dict(api=args.api_image, web=args.web_image, browser=args.browser_image,
                minio=args.minio_image, postgres=postgres_image())
    # Resolve every image before creating a directory, network, container or credential.
    images = {name: command(['docker', 'image', 'inspect', '--format', '{{.Id}}', tag],
                           capture=True).stdout.strip() for name, tag in tags.items()}
    if not all(value.startswith('sha256:') and len(value) == 71 for value in images.values()):
        raise RuntimeError('Image inspection did not return immutable IDs')
    evidence = args.evidence.resolve()
    evidence.mkdir(parents=True, exist_ok=False)
    suffix = uuid.uuid4().hex[:12]
    created, names, network = [], {}, None
    with tempfile.TemporaryDirectory(prefix='fl-org-ui-') as directory:
        temporary = Path(directory)
        temporary.chmod(0o700)
        secrets_dir, fixture_dir, presets_dir = [temporary / name for name in ('secrets', 'fixture', 'presets')]
        for path in (secrets_dir, fixture_dir, presets_dir):
            path.mkdir(mode=0o700)
        secret_values = {}
        for name in ('postgres_password', 'minio_user', 'minio_password', 'admin_password', 'session_key',
                     *(f'client_{index}' for index in range(5))):
            secret_values[name] = secrets.token_urlsafe(36)
            path = secrets_dir / name
            path.write_text(secret_values[name], encoding='utf-8')
            path.chmod(0o600)
        secret_values['auth_limit_key'] = secrets.token_hex(32)
        limit_key = secrets_dir / 'auth_limit_key'
        limit_key.write_text(secret_values['auth_limit_key'], encoding='utf-8')
        limit_key.chmod(0o600)
        command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                 '-keyout', str(secrets_dir / 'server.key'), '-out', str(secrets_dir / 'server.crt'),
                 '-days', '1', '-subj', '/CN=disposable-organization-api',
                 '-addext', 'subjectAltName=DNS:api'], capture=True)
        (secrets_dir / 'server.key').chmod(0o600)
        (presets_dir / 'presets.json').write_text('[]', encoding='utf-8')
        actors = {key: dict(username='qa-' + key, name=name, role=role, password=secrets.token_urlsafe(32))
                  for key, name, role in [('manager', 'QA Руководитель', 'researcher'),
                                         ('member', 'QA Участник', 'researcher'),
                                         ('capped', 'QA Наблюдатель', 'viewer'),
                                         ('orgadmin', 'QA Администратор', 'researcher'),
                                         ('outsider', 'QA Другая организация', 'researcher')]}
        fixture = dict(kind='disposable-organization-ui', admin=dict(username='admin', password=secret_values['admin_password']),
                       actors=actors, organizations={key: dict(id=str(uuid.uuid4()), name='QA Организация ' + key.upper()) for key in ('a', 'b')})
        fixture_path = fixture_dir / 'fixture.json'
        fixture_path.write_text(json.dumps(fixture, ensure_ascii=False), encoding='utf-8')
        fixture_path.chmod(0o600)
        redactions = list(secret_values.values()) + [actor['password'] for actor in actors.values()]

        def mount(source, target, readonly=True):
            return ['--mount', f'type=bind,source={source},target={target}' + (',readonly' if readonly else '')]

        def create(label, image, extra, arguments=()):
            result = command(['docker', 'create', '--pull=never', '--name', f'fl-org-ui-{suffix}-{label}',
                              '--network', network, '--memory', '256m', '--cpus', '1', *extra, image, *arguments], capture=True)
            container = result.stdout.strip()
            created.append(container)
            names[label] = container
            return container

        def start(container, attach=False, timeout=60):
            result = command(['docker', 'start', *(['--attach'] if attach else []), container], capture=True, timeout=timeout)
            if attach:
                state = json.loads(command(['docker', 'inspect', '--format', '{{json .State}}', container], capture=True).stdout)
                if state.get('Status') != 'exited' or state.get('ExitCode') != 0:
                    raise RuntimeError(f"Test container did not exit successfully: status={state.get('Status')}, exit={state.get('ExitCode')}")
            return result

        def run_node(label, file, mode=None, writable=False, storage=False):
            extra = ['--memory', '700m', '--shm-size', '256m',
                     *mount(ROOT / 'tests/e2e', '/work'), *mount(fixture_dir, '/run/qa', not writable),
                     *mount(evidence, '/evidence', False), '-e', 'FL_ORGANIZATION_UI_ISOLATED=1',
                     '-e', 'QA_API_HOST=api', '-e', 'WEB_HOST=web', '--entrypoint', 'node']
            if storage:
                extra += mount(secrets_dir / 'minio_user', '/run/secrets/minio_user')
                extra += mount(secrets_dir / 'minio_password', '/run/secrets/minio_password')
            container = create(label, images['browser'], extra, ['/work/' + file, *([mode] if mode else [])])
            start(container, attach=True, timeout=360)
            print('PASS: ' + label)

        try:
            network = command(['docker', 'network', 'create', '--internal', 'fl-org-ui-' + suffix], capture=True).stdout.strip()
            pg = create('postgres', images['postgres'], ['--network-alias', 'postgres', '--memory', '512m',
                '--tmpfs', '/var/lib/postgresql/data:rw,noexec,nosuid,size=512m',
                *mount(secrets_dir, '/run/secrets'), '-e', 'POSTGRES_USER=flrp', '-e', 'POSTGRES_DB=release_check',
                '-e', 'POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password'])
            start(pg); wait_postgres(pg)
            minio = create('minio', images['minio'], ['--network-alias', 'minio', '--memory', '512m',
                '--tmpfs', '/data:rw,noexec,nosuid,size=256m', *mount(secrets_dir, '/run/secrets'),
                '--entrypoint', '/bin/sh'], ['/opt/fl-entrypoint.sh', 'server', '/data'])
            start(minio)
            run_node('storage', 'organization-projects-real-support.cjs', 'storage', storage=True)
            common = [*mount(secrets_dir, '/run/secrets'), '-e', 'FL_PG_HOST=postgres:5432',
                      '-e', 'FL_PG_DATABASE=release_check', '-e', 'FL_SECRETS_DIR=/run/secrets',
                      '-e', 'FL_S3_ENDPOINT=minio:9000', '-e', 'FL_S3_BUCKET=organization-ui-check']
            bootstrap = create('bootstrap', images['api'], [*common, *mount(presets_dir, '/presets'),
                *mount(ROOT / 'db/migrations', '/migrations'), '--entrypoint', '/usr/local/bin/bootstrap'])
            start(bootstrap, attach=True, timeout=120)
            api = create('api', images['api'], [*common, '--network-alias', 'api',
                '-e', 'FL_AI_ENABLED=false', '-e', 'FL_ORGANIZATIONS_ENABLED=true',
                '-e', 'FL_ORGANIZATION_PROJECTS_ENABLED=true', '--entrypoint', '/usr/local/bin/api'])
            start(api)
            web = create('web', images['web'], ['--network-alias', 'web', '--memory', '128m'])
            start(web)
            run_node('setup', 'organization-projects-real-support.cjs', 'setup', writable=True)
            prepared = json.loads(fixture_path.read_text(encoding='utf-8'))
            rows = []
            for org, actor, role in [('a', 'member', 'researcher'), ('a', 'capped', 'researcher'),
                                     ('a', 'orgadmin', 'admin'), ('b', 'outsider', 'researcher')]:
                org_id = str(uuid.UUID(prepared['organizations'][org]['id']))
                user_id = str(uuid.UUID(prepared['actors'][actor]['id']))
                if org_id != fixture['organizations'][org]['id']:
                    raise RuntimeError('Setup unexpectedly changed an organization fixture ID')
                rows.append(f"('{org_id}','{user_id}','{role}')")
            # No SQL endpoint is exposed to browsers. Only these four initial memberships
            # are seeded; current API cannot enroll users until the invitations slice.
            sql = ("BEGIN; DO $$ BEGIN IF current_database() <> 'release_check' THEN "
                   "RAISE EXCEPTION 'not disposable'; END IF; END $$; "
                   "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES "
                   + ','.join(rows) + '; COMMIT;')
            subprocess.run(['docker', 'exec', '-i', pg, 'psql', '-U', 'flrp', '-d', 'release_check',
                            '-v', 'ON_ERROR_STOP=1'], input=sql, text=True, check=True,
                           capture_output=True, timeout=30)
            run_node('browser', args.browser_check + '.cjs')
            versions = command(['docker', 'exec', pg, 'psql', '-U', 'flrp', '-d', 'release_check',
                                '-At', '-c', 'SELECT version FROM schema_migrations ORDER BY version'], capture=True).stdout.splitlines()
            expected = [path.name.removesuffix('.up.sql') for path in sorted((ROOT / 'db/migrations').glob('*.up.sql'))]
            if versions != expected:
                raise RuntimeError('Bootstrap ledger does not match source migrations')
            (evidence / 'environment.json').write_text(json.dumps(dict(images=images, tags=tags,
                migrations=versions, browser_check=args.browser_check, seeded_memberships=4,
                readyz='PASS', live_resources_used=False), indent=2), encoding='utf-8')
        finally:
            try:
                for label, container in names.items():
                    result = subprocess.run(['docker', 'logs', container], stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=15)
                    text = result.stdout + result.stderr
                    for value in redactions:
                        text = text.replace(value, '[redacted]')
                    (evidence / (label + '.log')).write_text(text, encoding='utf-8')
            finally:
                remove_resources(created, network)
                (evidence / 'cleanup.json').write_text(json.dumps(dict(removed_created_containers=len(created),
                    removed_created_network=network is not None)), encoding='utf-8')
    if temporary.exists():
        raise RuntimeError('Temporary credential directory still exists after cleanup')
    cleanup_path = evidence / 'cleanup.json'
    cleanup = json.loads(cleanup_path.read_text(encoding='utf-8'))
    cleanup['temporary_credentials_removed'] = True
    cleanup_path.write_text(json.dumps(cleanup), encoding='utf-8')
    print('PASS: exact created containers/network and temporary credentials removed')


if __name__ == '__main__':
    main()
