import unittest
from unittest.mock import patch
from verify_organization_upgrade import apply, disposable, migration_sql


class UpgradeGuards(unittest.TestCase):
    def test_live_or_non_id_target_never_executes_command(self):
        live = 'a' * 64
        for target in [live, 'fl-platform-postgres-1', '', 'b' * 63, '../live']:
            with self.subTest(target=target), patch('verify_organization_upgrade.command') as execute:
                with self.assertRaises(ValueError):
                    apply(target, 'flrp', [], [], live)
                execute.assert_not_called()
        disposable('b' * 64, live)

    def test_unknown_or_reordered_ledger_refused(self):
        migrations = [('000001_one', 'SELECT 1;'), ('000002_two', 'SELECT 2;')]
        for applied in [['999999_unknown'], ['000002_two'], ['000001_one', '000002_two', '000003_extra']]:
            with self.subTest(applied=applied), self.assertRaises(ValueError):
                migration_sql(migrations, applied)

    def test_only_pending_sql_is_applied_under_lock(self):
        migrations = [('000001_one', 'SELECT 1001;'), ('000002_two', 'SELECT 1002;')]
        text = migration_sql(migrations, ['000001_one'])
        self.assertNotIn('SELECT 1001;', text)
        self.assertLess(text.index('pg_advisory_xact_lock'), text.index('SELECT 1002;'))
        self.assertTrue(text.startswith('BEGIN;'))
        self.assertTrue(text.endswith('COMMIT;'))
        repeat = migration_sql(migrations, [name for name, _ in migrations])
        self.assertNotIn('INSERT INTO', repeat)
        self.assertNotIn('SELECT 1002;', repeat)

    def test_migration_name_cannot_inject_sql(self):
        with self.assertRaises(ValueError):
            migration_sql([("000001_bad');DROP TABLE users;--", 'SELECT 1;')], [])


if __name__ == '__main__':
    unittest.main()
