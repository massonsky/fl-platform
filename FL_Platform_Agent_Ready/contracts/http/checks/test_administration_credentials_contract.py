import unittest

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

from schema_loader import ROOT, load, resolve


class AdministrationCredentialContractTests(unittest.TestCase):
    def test_credential_retry_and_conflict_contracts(self):
        spec = resolve(load('administration.yaml'), ROOT / 'administration.yaml')
        for name in ('Users', 'User', 'Password'):
            operation = spec[name]['post']
            self.assertIn('current authorization', operation['description'])
            for status, code, retry in [('429', 'RATE_LIMITED', 1),
                                        ('503', 'DEPENDENCY_UNAVAILABLE', 3)]:
                response = operation['responses'][status]
                header = Draft202012Validator(response['headers']['Retry-After']['schema'])
                header.validate(retry)
                with self.assertRaises(ValidationError):
                    header.validate(0)
                body = Draft202012Validator(response['content']['application/json']['schema'])
                sample = dict(code=code, message=code, request_id='request-1', retryable=True)
                body.validate(sample)
                with self.assertRaises(ValidationError):
                    body.validate(dict(sample, password_hash='must-not-be-returned'))
            conflict = operation['responses']['409']
            self.assertIn('VERSION_CONFLICT', conflict['description'])
            Draft202012Validator(conflict['content']['application/json']['schema']).validate(
                dict(code='VERSION_CONFLICT', message='VERSION_CONFLICT',
                     request_id='request-1', retryable=False))

    def test_admin_dto_never_accepts_prepared_or_claimed_identity(self):
        spec = resolve(load('administration.yaml'), ROOT / 'administration.yaml')
        inputs = {
            'Users': dict(username='new-user', name='New user', role='viewer', password='valid-new-password'),
            'User': dict(role='researcher', disabled=False, password='valid-new-password'),
            'Password': dict(current_password='old-password', password='valid-new-password'),
        }
        for name, valid in inputs.items():
            operation = spec[name]['post']
            check = Draft202012Validator(operation['requestBody']['content']['application/json']['schema'])
            check.validate(valid)
            for field in ('client', 'actor_id', 'password_hash', 'prepared', 'limits'):
                with self.subTest(operation=name, field=field), self.assertRaises(ValidationError):
                    check.validate(dict(valid, **{field: 'caller-value'}))
        update = spec['User']['post']['requestBody']['content']['application/json']['schema']
        Draft202012Validator(update).validate(dict(role='viewer', disabled=True))
