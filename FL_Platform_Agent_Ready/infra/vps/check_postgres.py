"""Migration and backup/restore smoke in a newly created disposable database."""
from pathlib import Path
import subprocess
import uuid

PREFIX = ['docker', 'compose', 'exec', '-T', 'postgres']


def run(args, data='', check=True):
    return subprocess.run(PREFIX + args, input=data, text=True, capture_output=True,
                          check=check, timeout=90)


def sql(db, query=None, data='', check=True):
    args = ['psql', '-X', '-U', 'flrp', '-d', db, '-v', 'ON_ERROR_STOP=1', '-At']
    if query is not None:
        args += ['-c', query]
    return run(args, data, check)


def table_count(db):
    return int(sql(db, "SELECT count(*) FROM information_schema.tables "
                      "WHERE table_schema='public'").stdout.strip())


def main():
    scratch = 'flrp_smoke_' + uuid.uuid4().hex[:12]
    created = False
    try:
        sql('postgres', f'CREATE DATABASE {scratch}')
        created = True
        up = Path('db/migrations/000001_init.up.sql').read_text()
        down = Path('db/migrations/000001_init.down.sql').read_text()
        sql(scratch, data='BEGIN;\n' + up + '\nCOMMIT;')
        assert table_count(scratch) == 12
        invalid = sql(scratch, "INSERT INTO users(external_subject,display_name,role) "
                              "VALUES ('smoke','smoke','not-a-role')", check=False)
        assert invalid.returncode != 0 and 'check constraint' in invalid.stderr
        sql(scratch, data='BEGIN;\n' + down + '\nCOMMIT;')
        assert table_count(scratch) == 0
        sql(scratch, data='BEGIN;\n' + up + '\nCOMMIT;')
        assert table_count(scratch) == 12
        sql(scratch, data='BEGIN;\n' + down + '\nCOMMIT;')
        dump = run(['pg_dump', '-U', 'flrp', '-d', 'flrp', '--no-owner']).stdout
        sql(scratch, data=dump)
        assert table_count(scratch) == table_count('flrp') == 12
        bad_auth = run(['env', 'PGPASSWORD=deliberately-invalid-smoke-password',
                        'psql', '-h', 'postgres', '-U', 'flrp', '-d', 'flrp',
                        '-c', 'SELECT 1'], check=False)
        assert bad_auth.returncode != 0 and 'authentication failed' in bad_auth.stderr
        good = run(['sh', '-c', 'export PGPASSWORD="$(cat /run/secrets/postgres_password)"; '
                    'exec psql -h postgres -U flrp -d flrp -Atc "SELECT 1"'])
        assert good.stdout.strip() == '1'
        print('PASS: 000001 up/down/up, invalid-role constraint, dump/restore, TCP auth +/-')
    finally:
        if created:
            sql('postgres', f'DROP DATABASE {scratch}')


if __name__ == '__main__':
    main()
