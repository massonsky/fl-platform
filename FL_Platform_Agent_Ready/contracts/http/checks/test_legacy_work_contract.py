import unittest

from jsonschema.exceptions import ValidationError
from openapi_schema_validator import OAS30Validator, oas30_format_checker
from schema_loader import ROOT, load, resolve


class LegacyWorkContractTests(unittest.TestCase):
    def test_consuming_errors_are_safe_and_keep_receipt_contract(self):
        spec = resolve(load('openapi.yaml'), ROOT / 'openapi.yaml')
        for path in ('/projects', '/projects/{projectId}/experiments', '/experiments/{experimentId}/runs'):
            with self.subTest(path=path):
                op = spec['paths'][path]['post']
                key = next(p for p in op['parameters'] if p['name'] == 'Idempotency-Key')
                self.assertTrue(key['required'])
                for status, code, retryable in [('403', 'LEGACY_LAB_NOT_ADMITTED', False),
                                                 ('503', 'DEPENDENCY_UNAVAILABLE', True),
                                                 ('404', 'NOT_FOUND', False)]:
                    schema = op['responses'][status]['content']['application/json']['schema']
                    check = OAS30Validator(schema, format_checker=oas30_format_checker)
                    actual = dict(code=code, message=code, request_id='request-work-check', retryable=retryable)
                    check.validate(actual)
                    with self.assertRaises(ValidationError):
                        check.validate(dict(actual, session_hash='private'))
                self.assertNotIn('security', op)
                self.assertFalse({'admission', 'legacy_lab', 'allow_legacy', 'compatibility'} &
                                 {p['name'] for p in op.get('parameters', [])})

    def test_existing_archive_and_cancel_shapes_are_preserved(self):
        spec = resolve(load('openapi.yaml'), ROOT / 'openapi.yaml')
        paths = spec['paths']
        for path in ('/projects', '/projects/{projectId}/experiments', '/projects/{projectId}/runs',
                     '/experiments/{experimentId}', '/runs/{runId}', '/runs/{runId}/rounds',
                     '/runs/{runId}/metrics', '/runs/{runId}/events', '/runs/{runId}/export'):
            self.assertIn('200', paths[path]['get']['responses'])
        cancel = paths['/runs/{runId}/cancel']['post']
        self.assertIn('200', cancel['responses'])
        self.assertTrue(next(p for p in cancel['parameters'] if p['name'] == 'Idempotency-Key')['required'])
        self.assertIn('application/zip', paths['/runs/{runId}/export']['get']['responses']['200']['content'])


if __name__ == '__main__':
    unittest.main()
