import unittest
from copy import deepcopy

from jsonschema.exceptions import ValidationError
from openapi_schema_validator import OAS30Validator, oas30_format_checker
from schema_loader import ROOT, load, resolve, validator


class LegacyLabContractTests(unittest.TestCase):
    def test_explicit_cas_input_bounds_and_no_identity_injection(self):
        change = validator('legacy-lab.yaml', 'Change')
        good = dict(status='active', revision=0, reason='Operator reviewed')
        change.validate(good)
        change.validate(dict(status='revoked', revision=2147483647, reason='я' * 240))
        cases = [dict(status='active', reason='Review'),
                 dict(status='active', revision=None, reason='Review'),
                 dict(status='not_admitted', revision=0, reason='Review')]
        cases += [dict(good, revision=v) for v in (-1, 0.5, 2147483648, 1e100, '0')]
        cases += [dict(good, reason=v) for v in ('', '   ', 'line\nbreak', '\x00bad', '\x7f', 'я' * 241)]
        cases += [dict(good, **{key: 'forged'}) for key in ('user_id', 'source', 'updated_at', 'can_create_legacy_work')]
        for bad in cases:
            with self.subTest(bad=bad), self.assertRaises(ValidationError):
                change.validate(bad)

    def test_metadata_grant_is_not_an_effective_role(self):
        admission = validator('legacy-lab.yaml', 'Admission')
        absent = dict(user_id='11111111-1111-4111-8111-111111111111', status='not_admitted',
                      revision=0, can_create_legacy_work=False)
        admission.validate(absent)
        active = dict(absent, status='active', revision=1, source='admin', updated_at='2026-11-14T12:00:00Z')
        admission.validate(active)  # A viewer/disabled target may have metadata but no effective right.
        admission.validate(dict(active, can_create_legacy_work=True))
        admission.validate(dict(active, status='revoked', can_create_legacy_work=False))
        for bad in [dict(absent, can_create_legacy_work=True), dict(absent, source='admin'),
                    dict(active, revision=0), dict(active, status='revoked', can_create_legacy_work=True),
                    dict(active, source='membership'), dict(active, SessionHash='private'),
                    dict(active, updated_at=None)]:
            with self.assertRaises(ValidationError):
                admission.validate(bad)
        missing = deepcopy(active)
        missing.pop('source')
        with self.assertRaises(ValidationError):
            admission.validate(missing)

    def test_routes_are_authenticated_cas_without_enable_switch(self):
        spec = resolve(load('openapi.yaml'), ROOT / 'openapi.yaml')
        own = spec['paths']['/me/legacy-lab']
        target = spec['paths']['/admin/users/{userId}/legacy-lab']
        self.assertEqual(set(own), {'get'})
        self.assertEqual(set(target), {'parameters', 'get', 'put'})
        self.assertEqual(spec['security'], [{'bearerAuth': []}])
        for op in (own['get'], target['get'], target['put']):
            self.assertNotIn('security', op)
            self.assertIn('200', op['responses'])
            for code in ('400', '401', '403', '404', '503'):
                self.assertIn(code, op['responses'])
        self.assertIn('409', target['put']['responses'])
        self.assertNotIn('parameters', target['put'])  # CAS, not an Idempotency-Key receipt API.
        self.assertNotIn('/account/legacy-lab', spec['paths'])
        self.assertEqual(set(target['put']['requestBody']['content']), {'application/json'})

    def test_actual_revision_conflict_envelope_matches_put_response(self):
        spec = resolve(load('openapi.yaml'), ROOT / 'openapi.yaml')
        schema = spec['paths']['/admin/users/{userId}/legacy-lab']['put']['responses']['409']['content']['application/json']['schema']
        response = OAS30Validator(schema, format_checker=oas30_format_checker)
        actual = dict(code='VERSION_CONFLICT', message='VERSION_CONFLICT',
                      request_id='11111111-1111-4111-8111-111111111111', retryable=False)
        response.validate(actual)
        with self.assertRaises(ValidationError):
            response.validate(dict(actual, session_hash='private'))


if __name__ == '__main__':
    unittest.main()
