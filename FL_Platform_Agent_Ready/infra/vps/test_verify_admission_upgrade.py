import copy
import json
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

from admission_upgrade_checks import bootstrap, check_backfill, corruption_check, image_id, revoke_admin
from check_migration_ledger import release_migrations
from verify_admission_upgrade import baseline, restore
from verify_organization_upgrade import apply, migration_sql


class AdmissionUpgradeGuards(unittest.TestCase):
    def test_live_or_alias_targets_cannot_mutate_or_start_bootstrap(self):
        live = 'a' * 64
        for target in [live, 'fl-platform-postgres-1', '', 'b' * 63, '../live']:
            with self.subTest(target=target), patch('admission_upgrade_checks.command') as execute:
                with self.assertRaises(ValueError):
                    bootstrap(target, 'flrp', 'sha256:' + 'c' * 64, None, live)
                with self.assertRaises(ValueError):
                    revoke_admin(target, 'flrp', live)
                with self.assertRaises(ValueError):
                    corruption_check(target, ['x'], live)
                execute.assert_not_called()
            with self.subTest(restore=target), patch('verify_admission_upgrade.subprocess.run') as execute:
                with self.assertRaises(ValueError):
                    restore(target, None, live)
                execute.assert_not_called()
            with patch('verify_organization_upgrade.command') as execute:
                with self.assertRaises(ValueError):
                    apply(target, 'flrp', [], [], live)
                execute.assert_not_called()

    def test_invalid_image_or_inspect_mismatch_is_rejected(self):
        with patch('admission_upgrade_checks.command') as execute:
            for image in ['--all', 'sha256:bad', 'bad image', '']:
                with self.subTest(image=image), self.assertRaises(ValueError):
                    image_id(image)
            execute.assert_not_called()
        with patch('admission_upgrade_checks.command', return_value=Mock(stdout='sha256:' + 'b' * 64)):
            with self.assertRaises(ValueError):
                image_id('sha256:' + 'a' * 64)
            self.assertEqual(image_id('fl-platform/control-plane:admission-check'), 'sha256:' + 'b' * 64)

    def test_unknown_ledger_and_live_schema_drift_are_rejected(self):
        migrations = [('000001_one', 'SELECT 1;'), ('000002_two', 'SELECT 2;')]
        for applied in [['999999_unknown'], ['000002_two'], ['000001_one', '000002_two', '000003_three']]:
            with self.subTest(applied=applied), self.assertRaises(ValueError):
                migration_sql(migrations, applied)
        with patch('verify_admission_upgrade.dump', return_value='SELECT 1;'), self.assertRaises(ValueError):
            baseline('a' * 64, ['000001_one'])

    def test_exact_admission_and_audit_sets_and_metadata(self):
        after = dict(status='active', revision=1, source='legacy_backfill')
        valid = dict(admissions=[dict(user_id='u1', updated_by=None, **after)], events=[dict(
            target_user_id='u1', actor_id=None, action='legacy_backfilled', before_json={},
            after_json=after, reason='Migration 000009: existing researcher/admin or legacy project owner')])
        check_backfill(['u1'], valid)
        cases = []
        for field in ('admissions', 'events'):
            missing, duplicate = copy.deepcopy(valid), copy.deepcopy(valid)
            missing[field] = []
            duplicate[field] += copy.deepcopy(duplicate[field])
            cases.extend([missing, duplicate])
        for field, key, value in [('admissions', 'revision', 2), ('admissions', 'source', 'bootstrap'),
                                  ('admissions', 'user_id', 'extra'), ('admissions', 'status', 'revoked'),
                                  ('events', 'action', 'changed'), ('events', 'before_json', {'status': 'revoked'}),
                                  ('events', 'actor_id', 'unexpected'), ('events', 'after_json', {})]:
            invalid = copy.deepcopy(valid)
            invalid[field][0][key] = value
            cases.append(invalid)
        for invalid in cases:
            with self.subTest(snapshot=invalid), self.assertRaises(ValueError):
                check_backfill(['u1'], invalid)

    def test_incomplete_migration_sequence_is_rejected(self):
        directory = Mock()
        sources = {'000001_one.up.sql': 'SELECT 1;', '000003_three.up.sql': 'SELECT 3;'}
        directory.glob.return_value = [Path(name) for name in sources]
        with patch.object(Path, 'read_text', lambda path, **kwargs: sources[path.name]):
            with self.assertRaises(ValueError):
                release_migrations(directory, 3)
            sources['000002_two.up.sql'] = ''
            directory.glob.return_value = [Path(name) for name in sources]
            with self.assertRaises(ValueError):
                release_migrations(directory, 3)

    def test_bootstrap_failure_still_cleans_exact_created_id(self):
        created = 'c' * 64
        calls = []

        def execute(args, **kwargs):
            calls.append(args)
            if args[1] == 'create':
                return Mock(stdout=created)
            if args[1] == 'inspect':
                return Mock(stdout=json.dumps(dict(Status='exited', ExitCode=1)))
            return Mock(stdout='')

        with patch('admission_upgrade_checks.command', side_effect=execute), self.assertRaises(ValueError):
            bootstrap('b' * 64, 'flrp', 'sha256:' + 'd' * 64, Path('/private-fixture'), 'a' * 64)
        self.assertEqual(calls[-1], ['docker', 'rm', '--force', '--volumes', created])
        self.assertIn('--pull=never', calls[0])
        self.assertIn('container:' + 'b' * 64, calls[0])
        self.assertNotIn('--publish', calls[0])


if __name__ == '__main__':
    unittest.main()
