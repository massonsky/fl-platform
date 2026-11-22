"""Local-only regression tests: no Docker command or live database is contacted."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import check_migration_ledger as ledger


class NormalizationTests(unittest.TestCase):
    def test_only_dump_comments_and_random_nonces_are_ignored(self):
        first = '-- first header\n\\restrict abc123\nCREATE TABLE t(id integer);\n\\unrestrict abc123\n'
        second = '-- another header\n\\restrict xyz987\nCREATE TABLE t(id integer);\n\\unrestrict xyz987\n'
        self.assertEqual(ledger.normalize_dump(first), ledger.normalize_dump(second))
        self.assertNotEqual(ledger.normalize_dump(first), ledger.normalize_dump(second.replace('integer', 'bigint')))

    def test_literal_function_body_and_sql_comments_are_not_rewritten(self):
        sql = "CREATE TABLE t(v text DEFAULT 'line\n-- literal data\n\\restrict abc\n');\n"
        sql += 'CREATE FUNCTION f() RETURNS text AS $body$\n-- function source\nSELECT \'ok\';\n$body$ LANGUAGE sql;\n'
        sql += "COMMENT ON TABLE t IS 'stored comment';\nSELECT E'escaped\\\'quote'; -- retained inline comment\n"
        self.assertEqual(sql, ledger.normalize_dump(sql))
        with self.assertRaises(ValueError):
            ledger.normalize_dump("CREATE TABLE t(v text DEFAULT 'unfinished\n")

    def test_prefix_must_match_and_drift_never_suggests_repairs(self):
        expected = ['000001_init', '000002_runtime']
        report = ledger.compare('SELECT 1;\n', 'SELECT 1;\n', expected[:1], expected)
        self.assertEqual(report['status'], 'missing_known_versions')
        self.assertEqual(report['missing_versions'], expected[1:])
        for applied in [['000002_runtime'], ['000001_init', '000003_unknown'], ['invalid\nmetadata']]:
            report = ledger.compare('SELECT 1;\n', 'SELECT 1;\n', applied, expected)
            self.assertEqual(report['status'], 'ledger_mismatch')
            self.assertEqual(report['missing_versions'], [])
        report = ledger.compare('SELECT 1;\n', 'SELECT 2;\n', expected[:1], expected)
        self.assertEqual(report['status'], 'schema_drift')
        self.assertEqual(report['missing_versions'], [])

    def test_explicit_maximum_and_gaps(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ['000001_init', '000002_runtime', '000003_later']:
                (root / (name + '.up.sql')).write_text('SELECT 1;', encoding='utf-8')
            self.assertEqual(len(ledger.release_migrations(root, 2)), 2)
            (root / '000002_runtime.up.sql').unlink()
            with self.assertRaises(ValueError):
                ledger.release_migrations(root, 3)


class DockerMock:
    network = 'a' * 64
    container = 'b' * 64
    schema = 'CREATE TABLE example(id integer);\n'

    def __init__(self, failure='', drift=False, cleanup_failure=False):
        self.calls, self.failure = [], failure
        self.drift, self.cleanup_failure = drift, cleanup_failure

    def __call__(self, args, data=None, check=True, timeout=90):
        self.calls.append((args, data))
        output, code = '', 0
        if args[:4] == ['docker', 'exec', '-i', 'live-postgres']:
            query = args[-1]
            if not query.startswith('SELECT ') or data is not None:
                raise AssertionError('Attempted live mutation')
            output = '170012\n' if 'server_version_num' in query else '[]\n'
        elif args[:3] == ['docker', 'inspect', '--format']:
            output = 'sha256:' + 'c' * 64
        elif args[:3] == ['docker', 'network', 'create']:
            output = self.network
        elif args[:2] == ['docker', 'create']:
            if self.failure == 'create':
                raise subprocess.CalledProcessError(1, args, stderr='not printed')
            output = self.container
        elif args[:2] == ['docker', 'exec'] and 'pg_dump' in args:
            output = self.schema + ('SELECT 2;\n' if self.drift and args[2] == self.container else '')
        elif args[:4] == ['docker', 'exec', '-i', self.container]:
            if self.failure == 'apply':
                raise subprocess.CalledProcessError(1, args, stderr='schema details must stay private')
            if data is None or 'CREATE TABLE schema_migrations' not in data or 'CREATE TABLE example' not in data:
                raise AssertionError('Reference migration not applied')
            if 'INSERT INTO schema_migrations' in data:
                raise AssertionError('Diagnostic must not repair any ledger')
        elif args[:3] == ['docker', 'rm', '--force']:
            code = 1 if self.cleanup_failure else 0
        return subprocess.CompletedProcess(args, code, output, '')


class IsolationTests(unittest.TestCase):
    def run_diagnostic(self, fake):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '000001_init.up.sql').write_text('CREATE TABLE example(id integer);', encoding='utf-8')
            args = argparse.Namespace(migrations=root, max_version=1, live_container='live-postgres', database='flrp', user='flrp')
            with patch.object(ledger, 'command', fake):
                return ledger.diagnostic(args)

    def assert_removed(self, fake, container=True):
        commands = [args for args, _ in fake.calls]
        self.assertIn(['docker', 'network', 'rm', fake.network], commands)
        if container:
            self.assertIn(['docker', 'rm', '--force', '--volumes', fake.container], commands)
        self.assertFalse(any(args[:2] == ['docker', 'rm'] and 'live-postgres' in args for args in commands))

    def test_success_uses_isolated_offline_reference_and_only_live_reads(self):
        fake = DockerMock()
        report, code = self.run_diagnostic(fake)
        self.assertEqual(code, 0)
        self.assertEqual(report['status'], 'missing_known_versions')
        self.assertNotIn('CREATE TABLE', json.dumps(report))
        create = next(args for args, _ in fake.calls if args[:2] == ['docker', 'create'])
        network = next(args for args, _ in fake.calls if args[:3] == ['docker', 'network', 'create'])
        self.assertIn('--internal', network)
        self.assertIn('--pull=never', create)
        self.assertIn('--tmpfs', create)
        self.assertNotIn('/opt/fl-platform/secrets', ' '.join(create))
        self.assert_removed(fake)

    def test_sql_failure_still_removes_both_created_resources(self):
        fake = DockerMock(failure='apply')
        report, code = self.run_diagnostic(fake)
        self.assertEqual((report['status'], code), ('check_failed', 2))
        self.assertNotIn('private', json.dumps(report))
        self.assert_removed(fake)

    def test_container_create_failure_removes_only_created_network(self):
        fake = DockerMock(failure='create')
        report, code = self.run_diagnostic(fake)
        self.assertEqual((report['status'], code), ('check_failed', 2))
        self.assert_removed(fake, container=False)
        self.assertFalse(any(args[:2] == ['docker', 'rm'] for args, _ in fake.calls))

    def test_schema_drift_and_cleanup_failure_fail_closed(self):
        fake = DockerMock(drift=True)
        report, code = self.run_diagnostic(fake)
        self.assertEqual((report['status'], code), ('schema_drift', 2))
        self.assertEqual(report['missing_versions'], [])
        self.assert_removed(fake)
        fake = DockerMock(cleanup_failure=True)
        report, code = self.run_diagnostic(fake)
        self.assertEqual((report['status'], code), ('cleanup_failed', 2))


if __name__ == '__main__':
    unittest.main()
