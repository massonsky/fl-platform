import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from schema_loader import ROOT, load, resolve


class LoginContractTests(unittest.TestCase):
    def test_login_bounds_and_server_owned_client(self):
        operation = resolve(load('runtime.yaml'), ROOT / 'runtime.yaml')['Session']['post']
        schema = operation['requestBody']['content']['application/json']['schema']
        check = Draft202012Validator(schema)
        check.validate(dict(username='CaseSensitive', password='x'))
        for value in [dict(username='', password='x'), dict(username='x' * 65, password='x'),
                      dict(username='x', password=''), dict(username='x', password='x' * 129),
                      dict(username='x', password='x', client='192.0.2.1')]:
            with self.subTest(value=value), self.assertRaises(ValidationError):
                check.validate(value)
        # Encoded byte and control-character bounds are covered by actual HTTP tests;
        # JSON Schema lengths count Unicode characters, not encoded bytes.
        self.assertIn('encoded bytes', schema['properties']['username']['description'])

    def test_login_retry_and_safe_error_contract(self):
        operation = resolve(load('runtime.yaml'), ROOT / 'runtime.yaml')['Session']['post']
        self.assertEqual(operation['security'], [])
        for status, code, retry in [('429', 'RATE_LIMITED', 1), ('503', 'DEPENDENCY_UNAVAILABLE', 3)]:
            response = operation['responses'][status]
            Draft202012Validator(response['headers']['Retry-After']['schema']).validate(retry)
            with self.assertRaises(ValidationError):
                Draft202012Validator(response['headers']['Retry-After']['schema']).validate(0)
            body = Draft202012Validator(response['content']['application/json']['schema'])
            sample = dict(code=code, message=code, request_id='request-1', retryable=True)
            body.validate(sample)
            with self.assertRaises(ValidationError):
                body.validate(dict(sample, token='must-not-be-returned'))
