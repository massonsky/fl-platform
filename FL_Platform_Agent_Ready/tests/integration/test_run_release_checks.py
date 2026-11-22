"""The release runner must not report PASS solely from docker start's exit code."""
import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from run_release_checks import require_success


class ExitEvidenceTests(unittest.TestCase):
    def test_stopped_success_is_required(self):
        states = [
            {'Status': 'running', 'ExitCode': 0, 'OOMKilled': False},
            {'Status': 'exited', 'ExitCode': 1, 'OOMKilled': False},
            {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': True},
            {},
        ]
        for state in states:
            with self.subTest(state=state), patch('run_release_checks.command',
                    return_value=SimpleNamespace(stdout=json.dumps(state))):
                with self.assertRaises(RuntimeError):
                    require_success('disposable-container-id')

    def test_completed_success_is_accepted(self):
        state = {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}
        with patch('run_release_checks.command',
                return_value=SimpleNamespace(stdout=json.dumps(state))):
            require_success('disposable-container-id')


if __name__ == '__main__':
    unittest.main()
