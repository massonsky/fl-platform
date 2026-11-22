"""Run release SQL tests in a private, disposable PostgreSQL; never mount live secrets."""
import json
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
GO_IMAGE = 'fl-platform/go-test:mvp'
DATABASE = 'release_check'


def command(args, *, capture=False, timeout=60):
    return subprocess.run(args, check=True, stdin=subprocess.DEVNULL,
                          capture_output=capture, text=True, timeout=timeout)


def postgres_image():
    """Use the same immutable image as Compose; refuse an unpinned fallback."""
    matches = re.findall(r'^\s+image: (postgres@sha256:[0-9a-f]{64})\s*$',
                         (ROOT / 'compose.yaml').read_text(encoding='utf-8'), re.M)
    if len(matches) != 1:
        raise ValueError('Expected exactly one digest-pinned PostgreSQL image')
    return matches[0]


def wait_postgres(container):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        result = subprocess.run(
            ['docker', 'exec', container, 'pg_isready', '-h', '127.0.0.1', '-U', 'flrp', '-d', DATABASE],
            stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=5)
        if result.returncode == 0:
            return
        time.sleep(0.5)
    raise TimeoutError('Disposable PostgreSQL did not become ready within 45 seconds')


def remove_resources(containers, network):
    failures = []
    resources = [('container', value) for value in reversed(containers)]
    if network:
        resources.append(('network', network))
    for kind, resource_id in resources:
        args = ['docker', 'rm', '-f', resource_id] if kind == 'container' else [
            'docker', 'network', 'rm', resource_id]
        try:
            command(args, capture=True)
        except (subprocess.SubprocessError, OSError):
            failures.append(f'{kind} {resource_id}')
    if failures:
        raise RuntimeError('Could not remove created test resources: ' + ', '.join(failures))


def require_success(container):
    state = json.loads(command(['docker', 'inspect', '--format', '{{json .State}}',
                                container], capture=True).stdout)
    if state.get('Status') != 'exited' or state.get('ExitCode') != 0 or state.get('OOMKilled'):
        raise RuntimeError('Isolated Go checks did not exit successfully')


def main():
    image = postgres_image()
    # Existing build/dependency images only: this runner never pulls or installs.
    command(['docker', 'image', 'inspect', image, GO_IMAGE], capture=True)
    suffix = uuid.uuid4().hex[:12]
    containers, network = [], None
    with tempfile.TemporaryDirectory(prefix='fl-release-check-') as directory:
        secret_directory = Path(directory)
        secret_directory.chmod(0o700)
        password = secret_directory / 'postgres_password'
        password.write_text(secrets.token_urlsafe(48), encoding='utf-8')
        password.chmod(0o600)
        try:
            network = command(['docker', 'network', 'create', '--internal',
                               'fl-release-check-' + suffix], capture=True).stdout.strip()
            pg = command([
                'docker', 'create', '--pull=never', '--name', 'fl-release-pg-' + suffix,
                '--network', network, '--network-alias', 'release-postgres',
                '--memory', '512m', '--cpus', '1',
                '--tmpfs', '/var/lib/postgresql/data:rw,noexec,nosuid,size=512m',
                '--mount', f'type=bind,source={secret_directory},target=/run/secrets,readonly',
                '-e', 'POSTGRES_USER=flrp', '-e', 'POSTGRES_DB=' + DATABASE,
                '-e', 'POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password', image,
            ], capture=True).stdout.strip()
            containers.append(pg)
            command(['docker', 'start', pg], capture=True)
            wait_postgres(pg)
            go = command([
                'docker', 'create', '--pull=never', '--name', 'fl-release-go-' + suffix,
                '--network', network, '--memory', '768m', '--cpus', '1.5',
                '--mount', f'type=bind,source={ROOT},target=/src,readonly',
                '--mount', f'type=bind,source={ROOT / "db/migrations"},target=/migrations,readonly',
                '--mount', f'type=bind,source={secret_directory},target=/run/secrets,readonly',
                '-w', '/src/services/control-plane', '-e', 'FL_BOOTSTRAP_TEST_ISOLATED=1',
                '-e', 'FL_ORGANIZATION_TEST_ISOLATED=1',
                '-e', 'FL_PROJECT_ACCESS_TEST_ISOLATED=1',
                '-e', 'FL_LEGACY_LAB_TEST_ISOLATED=1',
                '-e', 'FL_CREDENTIAL_TEST_ISOLATED=1',
                '-e', 'FL_TENANT_AGENT_TEST_ISOLATED=1',
                '-e', 'FL_PG_DATABASE=' + DATABASE, '-e', 'FL_PG_HOST=release-postgres:5432',
                '-e', 'FL_SECRETS_DIR=/run/secrets', '-e', 'GOTOOLCHAIN=local',
                '-e', 'GOPROXY=off', '-e', 'GOSUMDB=off', '--entrypoint', 'go', GO_IMAGE,
                'test', '-mod=readonly', '-tags', 'integration', '-p', '1', '-timeout', '5m',
                '-count=1', '-v', './cmd/bootstrap', './internal/organization', './internal/projectaccess', './internal/legacylab', './internal/credentials', './internal/identity/adapters', './internal/administration/adapters', './internal/tenantagent', './internal/platform/transport/http',
            ], capture=True).stdout.strip()
            containers.append(go)
            command(['docker', 'start', '--attach', go], timeout=660)
            require_success(go)
            print('PASS: isolated bootstrap, organization, AI HTTP and dependency recovery SQL tests')
        finally:
            remove_resources(containers, network)
    print('PASS: created containers, internal network and temporary credentials removed')


if __name__ == '__main__':
    main()
