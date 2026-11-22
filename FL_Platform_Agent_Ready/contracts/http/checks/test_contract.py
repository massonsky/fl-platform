import unittest

from jsonschema.exceptions import ValidationError
from openapi_spec_validator import validate

from reference_rules import ApiFailure, Receipts, authorize, parse_body, validate_config
from schema_loader import ROOT, load, resolve, validator
from test_project_access_contract import ProjectAccessContractTests  # Included by the existing Docker unittest entrypoint.
from test_legacy_lab_contract import LegacyLabContractTests
from test_legacy_work_contract import LegacyWorkContractTests
from test_login_contract import LoginContractTests
from test_administration_credentials_contract import AdministrationCredentialContractTests


def config():
    return dict(schema_version=1, dataset_id='digits', dataset_manifest_hash='a'*64,
        partition_manifest_hash='b'*64, model_name='mlp-64-32-10', model_revision='1',
        model_schema_hash='c'*64, initial_weights_hash='d'*64, runtime_manifest_hash='e'*64,
        algorithm='fedavg', clients=5, rounds=25, seed=11, local_epochs=3,
        batch_size=25, learning_rate=0.12, mu=0, optimizer='sgd', partition='balanced',
        selection_strategy='all', completion_policy={'mode': 'strict'},
        round_timeout_seconds=300, lease_seconds=60)


class ContractTests(unittest.TestCase):
    def test_openapi_and_all_external_refs(self):
        spec = load('openapi.yaml')
        validate(spec, base_uri=(ROOT/'openapi.yaml').as_uri())
        resolved = resolve(spec, ROOT/'openapi.yaml')
        validate(resolved)
        baseline = {'/session', '/me', '/presets', '/projects',
            '/projects/{projectId}/experiments', '/projects/{projectId}/runs',
            '/experiments/{experimentId}', '/experiments/{experimentId}/runs',
            '/runs/{runId}', '/runs/{runId}/rounds', '/runs/{runId}/metrics',
            '/runs/{runId}/events', '/runs/{runId}/cancel', '/runs/{runId}/export',
            '/admin/users', '/admin/users/{userId}', '/admin/agents',
            '/admin/agents/{agentId}', '/admin/events', '/account/password',
            '/metrics', '/healthz', '/readyz'}
        self.assertTrue(baseline <= set(spec['paths']))

    def test_all_mutations_have_key_and_all_business_routes_have_auth_errors(self):
        spec = resolve(load('openapi.yaml'), ROOT/'openapi.yaml')
        self.assertEqual(spec['security'], [{'bearerAuth': []}])
        ids = set()
        for path, item in spec['paths'].items():
            for method in ('get', 'post', 'put', 'patch', 'delete'):
                if method not in item: continue
                op = item[method]
                self.assertNotIn(op['operationId'], ids)
                ids.add(op['operationId'])
                if path == '/session' and method == 'post':
                    self.assertEqual(op['security'], [])
                    self.assertEqual(op['operationId'], 'login')
                    continue
                if path in ('/healthz', '/readyz'):
                    self.assertEqual(op['security'], [])
                    continue
                self.assertNotIn('security', op)
                if path == '/session' and method == 'delete':
                    for code in ('401', '500', '503'):
                        self.assertIn(code, op['responses'])
                    continue
                for code in ('400', '401', '403', '404', '429', '500', '503'):
                    self.assertIn(code, op['responses'], path)
                if method == 'post' and not path.startswith('/ai/'):
                    key = next(p for p in op['parameters'] if p['name'] == 'Idempotency-Key')
                    self.assertTrue(key['required'])
                elif op['operationId'] == 'sendChatMessage':
                    body = op['requestBody']['content']['application/json']['schema']
                    self.assertIn('request_id', body['required'])
                    self.assertEqual(body['properties']['request_id']['format'], 'uuid')
                elif method == 'post':
                    self.assertIn(op['operationId'], {'createConversation',
                        'cancelChatGeneration', 'checkAIProviderModels'})

    def test_all_lists_have_limit_cursor_and_page_envelopes(self):
        spec = resolve(load('openapi.yaml'), ROOT/'openapi.yaml')
        paged = {'/projects', '/projects/{projectId}/experiments',
            '/projects/{projectId}/runs', '/runs/{runId}/rounds',
            '/runs/{runId}/metrics', '/runs/{runId}/events',
            '/admin/users', '/admin/agents', '/admin/events'}
        bounded = {'/ai/providers': 20, '/ai/conversations': 100, '/ai/assistants': 50}
        foundation = {'/organizations', '/organizations/{organizationId}/members'}
        project_pages = {'/organizations/{organizationId}/projects': 'context',
            '/organizations/{organizationId}/projects/{projectId}/access': 'project',
            '/organizations/{organizationId}/projects/{projectId}/access/candidates': 'project'}
        checked = set()
        for path, item in spec['paths'].items():
            op = item.get('get', {})
            if not op.get('operationId', '').startswith('list'): continue
            schema = op['responses']['200']['content']['application/json']['schema']
            self.assertEqual(schema['properties']['items']['type'], 'array')
            self.assertIn('items', schema['required'])
            checked.add(path)
            if path in paged:
                self.assertEqual({'limit', 'cursor'} & {p['name'] for p in op['parameters']}, {'limit', 'cursor'})
                self.assertEqual(set(schema['required']), {'items', 'next_cursor'})
            elif path in project_pages:
                self.assertEqual({'limit', 'cursor'} & {p['name'] for p in op['parameters']}, {'limit', 'cursor'})
                self.assertEqual(set(schema['required']), {'items', 'next_cursor', project_pages[path]})
                self.assertEqual(schema['properties']['items']['maxItems'], 100)
            elif path in bounded:
                self.assertEqual(schema['properties']['items']['maxItems'], bounded[path])
            else:
                self.assertIn(path, foundation, 'Document pagination/limits for this new list')
                self.assertEqual(schema['required'], ['items'])
        self.assertEqual(checked, paged | set(bounded) | foundation | set(project_pages))

    def test_nir_configuration_and_fedprox_zero_or_positive(self):
        c = config()
        validate_config(c)
        for mu in (0, 0.1):
            c.update(algorithm='fedprox', mu=mu)
            validate_config(c)

    def test_dirichlet_and_quorum(self):
        c = config()
        c.update(partition='dirichlet', dirichlet_alpha=0.5,
                 completion_policy={'mode': 'quorum', 'min_clients': 3})
        validate_config(c)

    def test_bad_config_scalars(self):
        for key, value in [('clients', 0), ('rounds', 10001), ('seed', -1),
                ('seed', 2**32), ('learning_rate', 0), ('mu', -0.1),
                ('mu', 0.1), ('optimizer', 'arbitrary-code'), ('clients', True),
                ('schema_version', 2), ('dataset_manifest_hash', 'not-a-hash')]:
            with self.subTest(key=key, value=value):
                c = config()
                c[key] = value
                with self.assertRaises(ValidationError): validate_config(c)

    def test_unknown_and_missing_config_fields(self):
        c = config()
        c['dataset_path'] = '/private/data'
        with self.assertRaises(ValidationError): validate_config(c)
        c = config()
        del c['runtime_manifest_hash']
        with self.assertRaises(ValidationError): validate_config(c)

    def test_partition_alpha_rules(self):
        for partition, alpha in [('balanced', 0.5), ('dirichlet', None), ('dirichlet', 0)]:
            c = config()
            c['partition'] = partition
            if alpha is not None: c['dirichlet_alpha'] = alpha
            with self.assertRaises(ValidationError): validate_config(c)

    def test_cross_field_policy_and_ttl(self):
        for change in [{'completion_policy': {'mode': 'quorum', 'min_clients': 6}},
                       {'lease_seconds': 600}]:
            c = config()
            c.update(change)
            with self.assertRaises(ApiFailure) as result: validate_config(c)
            self.assertEqual(result.exception.code, 'INVALID_CONFIG')

    def test_strict_cannot_carry_quorum_threshold(self):
        c = config()
        c['completion_policy']['min_clients'] = 1
        with self.assertRaises(ValidationError): validate_config(c)

    def test_strict_json_parser(self):
        self.assertEqual(parse_body(b'{"name":"valid"}'), {'name': 'valid'})
        for raw in [b'{"a":1,"a":2}', b'{"x":NaN}', b'{"x":Infinity}',
                    b'{"x":1e999}', b'\xff', b'{bad json}']:
            with self.subTest(raw=raw), self.assertRaises(ApiFailure): parse_body(raw)
        with self.assertRaises(ApiFailure) as result: parse_body(b'x'*65537)
        self.assertEqual(result.exception.status, 413)

    def test_owner_and_project_injection_rejected(self):
        project = {'slug': 'demo', 'name': 'Demo', 'owner_id': 'another-user'}
        with self.assertRaises(ValidationError): validator('config.yaml', 'ProjectInput').validate(project)
        experiment = {'name': 'Demo', 'config': config(), 'project_id': 'another-project'}
        with self.assertRaises(ValidationError): validator('config.yaml', 'ExperimentInput').validate(experiment)

    def test_authorization_reference_matrix(self):
        for role in ('researcher', 'admin', 'viewer'):
            p = {'id': 'owner', 'role': role}
            authorize(p, 'owner')
            with self.assertRaises(ApiFailure) as result: authorize(p, 'other')
            self.assertEqual(result.exception.status, 404)
        with self.assertRaises(ApiFailure) as result: authorize(None, 'owner')
        self.assertEqual(result.exception.status, 401)
        with self.assertRaises(ApiFailure) as result: authorize({'id': 'owner', 'role': 'viewer'}, 'owner', True)
        self.assertEqual(result.exception.status, 403)

    def test_idempotency_replay_conflict_and_principal_scope(self):
        receipts = Receipts()
        p = {'id': 'owner', 'role': 'researcher'}
        original = receipts.apply(p, 'owner', 'queueRun', 'test-key', '/experiments/1/runs', {})
        again = receipts.apply(p, 'owner', 'queueRun', 'test-key', '/experiments/1/runs', {})
        self.assertEqual(original, again)
        self.assertEqual(receipts.mutations, 1)
        with self.assertRaises(ApiFailure) as result:
            receipts.apply(p, 'owner', 'queueRun', 'test-key', '/experiments/2/runs', {})
        self.assertEqual(result.exception.code, 'IDEMPOTENCY_CONFLICT')
        q = {'id': 'other', 'role': 'researcher'}
        other = receipts.apply(q, 'other', 'queueRun', 'test-key', '/experiments/3/runs', {})
        self.assertNotEqual(other, original)
        with self.assertRaises(ApiFailure):
            receipts.apply(p, 'revoked', 'queueRun', 'test-key', '/experiments/1/runs', {})
        self.assertEqual(receipts.mutations, 2)

    def test_cursor_page_shapes(self):
        page = validator('resources.yaml', 'RunPage')
        page.validate({'items': [], 'next_cursor': None})
        for value in [[], {'items': []}, {'items': None, 'next_cursor': None},
                      {'items': [], 'next_cursor': ''}, {'items': [], 'next_cursor': '../forged'},
                      {'items': [], 'next_cursor': 'a'*2049}]:
            with self.assertRaises(ValidationError): page.validate(value)

    def test_safe_error_and_export_media(self):
        error = dict(code='NOT_FOUND', message='Resource not found', request_id='request-1', retryable=False)
        v = validator('common.yaml', 'ApiError')
        v.validate(error)
        for code in ('LAST_ADMIN', 'LAST_OWNER', 'DEPENDENCY_UNAVAILABLE', 'EXPORT_LIMIT_EXCEEDED', 'EXPORT_BUSY'):
            v.validate(dict(code=code, message=code, request_id='request-1',
                            retryable=code in ('DEPENDENCY_UNAVAILABLE', 'EXPORT_BUSY')))
        error['stack_trace'] = 'private internals'
        with self.assertRaises(ValidationError): v.validate(error)
        spec = resolve(load('openapi.yaml'), ROOT/'openapi.yaml')
        media = spec['paths']['/runs/{runId}/export']['get']['responses']['200']['content']
        self.assertEqual(set(media), {'application/zip'})

    def test_organization_contract_prevents_identity_injection_and_requires_cas(self):
        create = validator('organizations.yaml', 'CreateInput')
        create.validate({'name': 'Research team'})
        for value in [{}, {'name': ''}, {'name': 'Team', 'owner_id': 'other'},
                      {'name': 'Team', 'kind': 'personal'}]:
            with self.subTest(value=value), self.assertRaises(ValidationError):
                create.validate(value)
        change = validator('organizations.yaml', 'MemberChange')
        change.validate({'role': 'viewer', 'status': 'suspended', 'revision': 1})
        for value in [{'role': 'owner', 'status': 'active'},
                      {'role': 'platform_admin', 'status': 'active', 'revision': 1},
                      {'role': 'viewer', 'status': 'active', 'revision': 0},
                      {'role': 'viewer', 'status': 'active', 'revision': 1.5},
                      {'role': 'viewer', 'status': 'active', 'revision': 2**53}]:
            with self.subTest(value=value), self.assertRaises(ValidationError):
                change.validate(value)
        error = validator('organizations.yaml', 'ApiError')
        for code in ('ORGS_NOT_CONFIGURED', 'LAST_OWNER', 'VERSION_CONFLICT'):
            error.validate(dict(code=code, message=code, request_id='request-1',
                                retryable=code == 'ORGS_NOT_CONFIGURED'))
        spec = resolve(load('openapi.yaml'), ROOT/'openapi.yaml')
        create_op = spec['paths']['/organizations/{organizationId}']['put']
        self.assertIn('200', create_op['responses'])
        self.assertNotIn('post', spec['paths']['/organizations/{organizationId}/members'])


if __name__ == '__main__':
    unittest.main()
