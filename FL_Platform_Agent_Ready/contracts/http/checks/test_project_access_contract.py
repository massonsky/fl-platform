import unittest
from copy import deepcopy

from jsonschema.exceptions import ValidationError
from schema_loader import ROOT, load, resolve, validator


class ProjectAccessContractTests(unittest.TestCase):
    def test_create_and_cas_inputs_reject_identity_injection(self):
        create = validator('project-access-schemas.yaml', 'CreateInput')
        create.validate(dict(slug='shared-project', name='Shared'))
        for extra in ('owner_id', 'created_by', 'organization_id', 'visibility', 'access_mode'):
            with self.assertRaises(ValidationError):
                create.validate(dict(slug='shared-project', name='Shared', **{extra: 'forged'}))
        grant = validator('project-access-schemas.yaml', 'GrantInput')
        grant.validate(dict(role='viewer', status='revoked', revision=0))
        for bad in [dict(role='viewer', status='revoked'), dict(role='owner', status='active', revision=0),
                    dict(role='viewer', status='active', revision=-1),
                    dict(role='viewer', status='active', revision=1.5),
                    dict(role='viewer', status='active', revision=2147483648)]:
            with self.assertRaises(ValidationError): grant.validate(bad)
        visibility = validator('project-access-schemas.yaml', 'VisibilityInput')
        visibility.validate(dict(visibility='organization', acl_revision=1))
        for bad in [dict(visibility='organization'), dict(visibility='public', acl_revision=1),
                    dict(visibility='private', acl_revision=0)]:
            with self.assertRaises(ValidationError): visibility.validate(bad)

    def test_empty_page_preserves_create_capability_and_no_unsigned_cursor(self):
        page = validator('project-access-schemas.yaml', 'ProjectPage')
        good = dict(items=[], context=dict(organization_id='11111111-1111-4111-8111-111111111111',
                    organization_state='active', capabilities=dict(can_create_projects=True)), next_cursor=None)
        page.validate(good)
        for mutate in [lambda p: p.pop('context'), lambda p: p.update(items=None),
                       lambda p: p.update(next_cursor=''), lambda p: p.update(Next={'after_id':'private'}),
                       lambda p: p.update(SessionHash='private')]:
            bad = deepcopy(good); mutate(bad)
            with self.assertRaises(ValidationError): page.validate(bad)

    def test_shared_run_gate_and_safe_errors(self):
        caps = validator('project-access-schemas.yaml', 'Capabilities')
        good = dict(can_read=True, can_export=True, can_create_experiments=True,
                    can_start_runs=False, can_cancel_runs=True, can_manage_access=True)
        caps.validate(good)
        good['can_start_runs'] = True
        with self.assertRaises(ValidationError): caps.validate(good)
        error = validator('project-access-schemas.yaml', 'ApiError')
        for code in ('ORG_PROJECTS_NOT_CONFIGURED', 'VERSION_CONFLICT', 'INHERITED_ACCESS',
                     'MEMBER_NOT_ACTIVE', 'INVALID_CURSOR', 'DEPENDENCY_UNAVAILABLE'):
            error.validate(dict(code=code, message=code, request_id='request-1', retryable=False))
        spec = resolve(load('openapi.yaml'), ROOT/'openapi.yaml')
        prefix = '/organizations/{organizationId}/projects'
        self.assertIn('200', spec['paths'][prefix+'/{projectId}']['put']['responses'])
        self.assertNotIn(prefix+'/{projectId}/experiments', spec['paths'])
        self.assertNotIn(prefix+'/{projectId}/runs', spec['paths'])
        self.assertNotIn(prefix+'/{projectId}/export', spec['paths'])


if __name__ == '__main__':
    unittest.main()
