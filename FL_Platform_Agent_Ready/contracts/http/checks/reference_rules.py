"""Contract oracles only; not HTTP handlers, durable storage or JWT verification."""
import copy
import json
import math

from schema_loader import validator


class ApiFailure(ValueError):
    def __init__(self, status, code):
        self.status, self.code = status, code
        super().__init__(code)


def parse_body(raw):
    if len(raw) > 65536:
        raise ApiFailure(413, 'PAYLOAD_TOO_LARGE')

    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ApiFailure(400, 'INVALID_REQUEST')
            result[key] = value
        return result

    def invalid(_):
        raise ApiFailure(400, 'INVALID_REQUEST')

    try:
        result = json.loads(raw.decode('utf-8'), object_pairs_hook=pairs, parse_constant=invalid)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ApiFailure(400, 'INVALID_REQUEST') from exc

    def finite(value):
        if isinstance(value, float) and not math.isfinite(value):
            invalid(value)
        if isinstance(value, dict):
            for item in value.values(): finite(item)
        if isinstance(value, list):
            for item in value: finite(item)
    finite(result)
    return result


def validate_config(config):
    validator('config.yaml', 'RunConfig').validate(config)
    if config['lease_seconds'] > config['round_timeout_seconds']:
        raise ApiFailure(400, 'INVALID_CONFIG')
    policy = config['completion_policy']
    if policy['mode'] == 'quorum' and policy['min_clients'] > config['clients']:
        raise ApiFailure(400, 'INVALID_CONFIG')


def authorize(principal, owner, mutation=False):
    if principal is None:
        raise ApiFailure(401, 'UNAUTHORIZED')
    if owner != principal['id']:
        raise ApiFailure(404, 'NOT_FOUND')
    if principal['role'] not in ('researcher', 'admin', 'viewer'):
        raise ApiFailure(403, 'FORBIDDEN')
    if mutation and principal['role'] == 'viewer':
        raise ApiFailure(403, 'FORBIDDEN')


class Receipts:
    """Atomic reference model; implementation must use a database transaction."""
    def __init__(self):
        self.saved = {}
        self.mutations = 0

    def apply(self, principal, owner, operation, key, path, body):
        authorize(principal, owner, mutation=True)
        scope = (principal['id'], operation, key)
        # Fixtures are normalized objects, not a JCS implementation.
        fingerprint = (path, copy.deepcopy(body))
        if scope in self.saved:
            previous, result = self.saved[scope]
            if previous != fingerprint:
                raise ApiFailure(409, 'IDEMPOTENCY_CONFLICT')
            return copy.deepcopy(result)
        self.mutations += 1
        result = {'id': f'fixture-resource-{self.mutations}'}
        self.saved[scope] = (fingerprint, result)
        return copy.deepcopy(result)
