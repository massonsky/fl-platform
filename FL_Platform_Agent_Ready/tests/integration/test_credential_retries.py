"""Actual administration fixture helper; fake HTTP/time only, never a live endpoint."""
from contextlib import ExitStack
import importlib.util
import io
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.error

with patch.dict(os.environ, {'TEST_API_A': 'flrp-check-fixture-only'}):
    spec = importlib.util.spec_from_file_location('credential_fixture_admin', Path(__file__).with_name('administration.py'))
    admin = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(admin)


class Reply(io.BytesIO):
    def __init__(self, status, headers, body):
        super().__init__(json.dumps(body).encode())
        self.status, self.headers = status, headers


class Harness:
    def __init__(self, test, steps, oversleep=0):
        self.steps, self.clock, self.keys = steps, 0, 0
        self.calls, self.sleeps, self.responses = [], [], []
        self.body = {'username': 'qa-fixture', 'name': 'Original', 'role': 'researcher', 'password': 'fixture-only-password'}
        stack = ExitStack()
        test.addCleanup(stack.close)

        def key():
            self.keys += 1
            return '11111111-1111-4111-8111-' + str(self.keys).zfill(12)

        def sleep(delay):
            self.sleeps.append(delay)
            self.clock += delay + oversleep

        def send(request, timeout):
            self.calls.append({'url': request.full_url, 'method': request.method, 'body': request.data,
                               'headers': {key.lower(): value for key, value in request.header_items()}, 'timeout': timeout})
            test.assertLessEqual(len(self.calls), len(steps), 'unexpected extra request')
            step = steps[len(self.calls) - 1]
            self.clock += step.get('elapsed', 0)
            if 'effect' in step:
                step['effect']()
            if 'error' in step:
                raise step['error']
            status = step['status']
            headers = {'Retry-After': step['header']} if 'header' in step else {}
            response = Reply(status, headers, step.get('body', {'code': 'RATE_LIMITED'}))
            self.responses.append(response)
            if status >= 400:
                raise urllib.error.HTTPError(request.full_url, status, 'controlled', headers, response)
            return response

        stack.enter_context(patch.object(admin.time, 'monotonic', lambda: self.clock))
        stack.enter_context(patch.object(admin.time, 'sleep', sleep))
        stack.enter_context(patch.object(admin.uuid, 'uuid4', key))
        stack.enter_context(patch.object(admin.urllib.request, 'urlopen', send))

    def call(self, expected=201, retry=True, path='/admin/users', key=None):
        return admin.call(path, 'fixture-bearer', self.body, expected, key, retry429=retry)


class CredentialRetryTests(unittest.TestCase):
    def test_stable_request_and_exact_header_delays(self):
        steps = [{'status': 429, 'header': '2'}, {'status': 429, 'header': '1'}, {'status': 201, 'body': {'id': 'created'}}]
        f = Harness(self, steps)
        steps[0]['effect'] = lambda: f.body.update(name='Changed after first send')
        self.assertEqual(f.call(), {'id': 'created'})
        self.assertEqual(f.sleeps, [2, 1])
        self.assertEqual(f.keys, 1)
        self.assertEqual([call['timeout'] for call in f.calls], [30, 28, 27])
        for call in f.calls:
            self.assertEqual(call['body'], f.calls[0]['body'])
            self.assertEqual(json.loads(call['body'])['name'], 'Original')
            self.assertEqual(call['headers'], f.calls[0]['headers'])
            self.assertEqual(call['headers']['authorization'], 'Bearer fixture-bearer')
            self.assertEqual(call['method'], 'POST')
        self.assertTrue(all(response.closed for response in f.responses))

    def test_self_password_reuses_supplied_key(self):
        f = Harness(self, [{'status': 429, 'header': '6'}, {'status': 200, 'body': {'saved': True}}])
        self.assertEqual(f.call(200, path='/account/password', key='original-logical-key'), {'saved': True})
        self.assertEqual(f.keys, 0)
        self.assertEqual(f.sleeps, [6])
        self.assertEqual([item['headers']['idempotency-key'] for item in f.calls], ['original-logical-key'] * 2)

    def test_six_attempt_limit(self):
        f = Harness(self, [{'status': 429, 'header': '1'} for _ in range(6)])
        with self.assertRaises(AssertionError):
            f.call()
        self.assertEqual(len(f.calls), 6)
        self.assertEqual(f.sleeps, [1] * 5)
        self.assertEqual(f.keys, 1)

    def test_remaining_deadline_stops_retry(self):
        f = Harness(self, [{'status': 429, 'header': '20', 'elapsed': 2}, {'status': 429, 'header': '9'}])
        with self.assertRaises(AssertionError):
            f.call()
        self.assertEqual(len(f.calls), 2)
        self.assertEqual(f.sleeps, [20])

    def test_oversleep_never_sends_after_deadline(self):
        f = Harness(self, [{'status': 429, 'header': '1'}], oversleep=30)
        with self.assertRaisesRegex(AssertionError, 'deadline exhausted'):
            f.call()
        self.assertEqual(len(f.calls), 1)
        self.assertEqual(f.sleeps, [1])

    def test_bad_headers_and_thirty_second_delay_do_not_wait(self):
        for header in [None, '', '0', '31', '-1', '+1', '01', '1.0', '1 ', ' 1', '١', 'Wed, 21 Oct 2026 07:28:00 GMT', '30']:
            with self.subTest(header=header):
                f = Harness(self, [{'status': 429, 'header': header}])
                with self.assertRaises(AssertionError):
                    f.call()
                self.assertEqual(len(f.calls), 1)
                self.assertEqual(f.sleeps, [])
                self.doCleanups()

    def test_other_errors_do_not_retry(self):
        for status, code in [(503, 'DEPENDENCY_UNAVAILABLE'), (401, 'UNAUTHORIZED'), (403, 'FORBIDDEN'), (409, 'VERSION_CONFLICT'), (429, 'OTHER_ERROR')]:
            with self.subTest(status=status, code=code):
                f = Harness(self, [{'status': status, 'header': '1', 'body': {'code': code}}])
                with self.assertRaises(AssertionError):
                    f.call()
                self.assertEqual(len(f.calls), 1)
                self.assertEqual(f.sleeps, [])
                self.doCleanups()

    def test_network_error_is_propagated_once(self):
        error = urllib.error.URLError('controlled network failure')
        f = Harness(self, [{'error': error}])
        with self.assertRaises(urllib.error.URLError) as caught:
            f.call()
        self.assertIs(caught.exception, error)
        self.assertEqual(len(f.calls), 1)
        self.assertEqual(f.sleeps, [])

    def test_expected429_and_expected_denial_stay_one_shot(self):
        for status, code in [(429, 'RATE_LIMITED'), (403, 'FORBIDDEN')]:
            with self.subTest(status=status):
                f = Harness(self, [{'status': status, 'header': '1', 'body': {'code': code}}])
                self.assertEqual(f.call(status), {'code': code})
                self.assertEqual(len(f.calls), 1)
                self.assertEqual(f.sleeps, [])
                self.doCleanups()

    def test_opt_in_does_not_apply_to_unrelated_or_default_calls(self):
        for retry, path in [(False, '/admin/users'), (True, '/projects')]:
            with self.subTest(retry=retry, path=path):
                f = Harness(self, [{'status': 429, 'header': '1'}])
                with self.assertRaises(AssertionError):
                    f.call(retry=retry, path=path)
                self.assertEqual(len(f.calls), 1)
                self.assertEqual(f.sleeps, [])
                self.doCleanups()


if __name__ == '__main__':
    unittest.main()
