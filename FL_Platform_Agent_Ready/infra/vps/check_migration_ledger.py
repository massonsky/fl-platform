"""Read-only live public-schema/ledger comparison against an isolated PostgreSQL 17.

Never repairs the live ledger. Output contains only status, versions and hashes.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
VERSION = re.compile(r'^([0-9]{6})_[a-z][a-z0-9_]*$')
OBJECT_ID = re.compile(r'^[0-9a-f]{64}$')
LABEL = 'fl-platform.ledger-diagnostic=true'


def normalize_dump(raw):
    """Remove dump comments/nonces, preserving SQL, literals and function bodies."""
    output, quote, escaped = [], '', False
    for line in raw.splitlines(keepends=True):
        if not quote and (line.lstrip().startswith('--') or
                          re.fullmatch(r'\\(?:un)?restrict [A-Za-z0-9]+\r?\n?', line)):
            continue
        output.append(line)
        i = 0
        while i < len(line):
            if quote.startswith('$') or quote == '/*':
                end = line.find('*/' if quote == '/*' else quote, i)
                if end < 0:
                    break
                i, quote = end + (2 if quote == '/*' else len(quote)), ''
            elif quote:
                if line[i:i + 2] == quote * 2 or (escaped and line[i] == '\\'):
                    i += 2
                elif line[i] == quote:
                    quote, i = '', i + 1
                else:
                    i += 1
            elif line[i:i + 2] == '--':
                break
            elif line[i:i + 2] == '/*':
                quote, i = '/*', i + 2
            elif line[i] in "'\"":
                quote = line[i]
                escaped = quote == "'" and i > 0 and line[i - 1] in 'eE' and (i < 2 or not line[i - 2].isalnum())
                i += 1
            elif line[i] == '$' and (match := re.match(r'\$(?:[A-Za-z_][A-Za-z_0-9]*)?\$', line[i:])):
                quote, i = match.group(), i + len(match.group())
            else:
                i += 1
    if quote:
        raise ValueError('Incomplete schema dump')
    return ''.join(output)


def release_migrations(directory, maximum):
    if not 1 <= maximum <= 999999:
        raise ValueError('Invalid maximum version')
    selected = []
    for path in sorted(directory.glob('*.up.sql')):
        name = path.name.removesuffix('.up.sql')
        match = VERSION.fullmatch(name)
        if not match:
            raise ValueError('Invalid migration filename')
        number = int(match[1])
        if number <= maximum:
            if number != len(selected) + 1:
                raise ValueError('Gapped or duplicate migration sequence')
            selected.append((name, path.read_text(encoding='utf-8')))
    if len(selected) != maximum or any(not sql.strip() for _, sql in selected):
        raise ValueError('Incomplete release migrations')
    return selected


def compare(live_dump, reference_dump, applied, expected):
    live, reference = normalize_dump(live_dump), normalize_dump(reference_dump)
    valid = isinstance(applied, list) and all(isinstance(v, str) and VERSION.fullmatch(v) for v in applied)
    prefix = valid and applied == expected[:len(applied)]
    equal = live == reference
    status = 'schema_drift' if not equal else 'ledger_mismatch' if not prefix else 'missing_known_versions' if len(applied) < len(expected) else 'consistent'
    return {'status': status, 'schema_equal': equal, 'expected_versions': expected,
            'applied_versions': applied if valid else [],
            'missing_versions': expected[len(applied):] if equal and prefix else [],
            'live_schema_sha256': hashlib.sha256(live.encode()).hexdigest(),
            'reference_schema_sha256': hashlib.sha256(reference.encode()).hexdigest()}


def command(args, data=None, check=True, timeout=90):
    return subprocess.run(args, input=data, stdin=subprocess.DEVNULL if data is None else None,
                          capture_output=True, text=True, encoding='utf-8', check=check, timeout=timeout)


def sql_command(container, database, user, query=None):
    args = ['docker', 'exec', '-i', container, 'psql', '-X', '-U', user, '-d', database, '-v', 'ON_ERROR_STOP=1', '-At']
    return args + ['-c', query] if query is not None else args


def dump(container, database, user):
    return command(['docker', 'exec', container, 'pg_dump', '-U', user, '-d', database,
                    '--schema-only', '--no-owner', '--no-privileges', '--schema=public']).stdout


def diagnostic(args):
    report = {'status': 'check_failed', 'schema_equal': None}
    container_id, network_id, cleanup_ok = '', '', True
    try:
        migrations = release_migrations(args.migrations, args.max_version)
        expected = [name for name, _ in migrations]
        report['expected_versions'] = expected
        live_sql = lambda query: command(sql_command(args.live_container, args.database, args.user, query)).stdout.strip()
        if int(live_sql("SELECT current_setting('server_version_num')")) // 10000 != 17:
            report['status'] = 'unsupported_postgres_version'
            return report, 2
        applied = json.loads(live_sql("SELECT coalesce(json_agg(version ORDER BY version),'[]'::json) FROM public.schema_migrations"))
        live_dump = dump(args.live_container, args.database, args.user)
        image = command(['docker', 'inspect', '--format', '{{.Image}}', args.live_container]).stdout.strip()
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
            raise ValueError('Invalid local image identity')
        name = 'fl-ledger-check-' + uuid.uuid4().hex
        with tempfile.TemporaryDirectory(prefix='fl-ledger-check-') as directory:
            password_file = Path(directory) / 'postgres_password'
            descriptor = os.open(password_file, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(descriptor, 'w') as output:
                output.write(secrets.token_hex(32))
            try:
                network_id = command(['docker', 'network', 'create', '--internal', '--label', LABEL, name]).stdout.strip()
                if not OBJECT_ID.fullmatch(network_id):
                    raise ValueError('Invalid created network ID')
                container_id = command(['docker', 'create', '--pull=never', '--name', name, '--label', LABEL,
                    '--network', network_id, '--memory', '512m', '--cpus', '0.5', '--read-only',
                    '--tmpfs', '/var/lib/postgresql/data:rw,noexec,nosuid,size=256m',
                    '--tmpfs', '/var/run/postgresql:rw,noexec,nosuid,size=16m',
                    '--mount', 'type=bind,src=' + str(password_file) + ',dst=/run/secrets/password,readonly',
                    '-e', 'POSTGRES_PASSWORD_FILE=/run/secrets/password', '-e', 'POSTGRES_USER=flrp',
                    '-e', 'POSTGRES_DB=flrp', image]).stdout.strip()
                if not OBJECT_ID.fullmatch(container_id):
                    raise ValueError('Invalid created container ID')
                command(['docker', 'start', container_id])
                for _ in range(60):
                    # The image's temporary initialization server is Unix-only;
                    # TCP readiness avoids applying SQL while that server stops.
                    ready = command(['docker', 'exec', container_id, 'pg_isready', '-h', '127.0.0.1',
                                     '-U', 'flrp', '-d', 'flrp'], check=False, timeout=5)
                    if ready.returncode == 0:
                        break
                    time.sleep(1)
                else:
                    raise TimeoutError('Reference PostgreSQL startup')
                schema = 'BEGIN;\nCREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());\n'
                for _, migration in migrations:
                    schema += migration + '\n'
                command(sql_command(container_id, 'flrp', 'flrp'), schema + 'COMMIT;\n')
                report = compare(live_dump, dump(container_id, 'flrp', 'flrp'), applied, expected)
            finally:
                for identifier, removal in [(container_id, ['docker', 'rm', '--force', '--volumes']),
                                            (network_id, ['docker', 'network', 'rm'])]:
                    if identifier and OBJECT_ID.fullmatch(identifier):
                        try:
                            cleanup_ok = command(removal + [identifier], check=False).returncode == 0 and cleanup_ok
                        except (OSError, subprocess.SubprocessError):
                            cleanup_ok = False
    except (OSError, ValueError, subprocess.SubprocessError, TimeoutError):
        report['status'] = 'check_failed'
    if not cleanup_ok:
        report['status'] = 'cleanup_failed'
    return report, 0 if report['status'] in ('consistent', 'missing_known_versions') else 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--live-container', default='fl-platform-postgres-1')
    parser.add_argument('--database', default='flrp')
    parser.add_argument('--user', default='flrp')
    parser.add_argument('--max-version', type=int, default=6)
    parser.add_argument('--migrations', type=Path, default=ROOT / 'db/migrations')
    report, code = diagnostic(parser.parse_args())
    print(json.dumps(report, ensure_ascii=True, sort_keys=True))
    return code


if __name__ == '__main__':
    raise SystemExit(main())
