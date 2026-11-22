"""Administrative API regression against disposable two-process deployment."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import threading
import time
import urllib.error
import urllib.request
import uuid

BASE = 'http://' + os.environ['TEST_API_A'] + ':8080/api/v1'
assert os.environ['TEST_API_A'].startswith('flrp-check-')


def call(path, token='', body=None, status=200, key=None, *, retry429=False):
    headers = {'Content-Type': 'application/json', 'Idempotency-Key': key or str(uuid.uuid4())}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    # One logical request keeps its exact key/bytes/actor throughout retries.
    req = urllib.request.Request(BASE + path, headers=headers,
        data=None if body is None else json.dumps(body).encode(), method='GET' if body is None else 'POST')
    eligible = (retry429 and isinstance(body, dict) and isinstance(body.get('password'), str)
                and ((path == '/admin/users' and status == 201)
                     or (path == '/account/password' and status == 200)))
    deadline = time.monotonic() + 30
    for attempt in range(1, 7):
        remaining = deadline - time.monotonic()
        assert remaining > 0, 'Credential fixture deadline exhausted'
        try:
            response = urllib.request.urlopen(req, timeout=min(30, remaining))
        except urllib.error.HTTPError as error:
            response = error
        try:
            result = json.loads(response.read())
            actual, header = response.status, response.headers.get('Retry-After')
        finally:
            response.close()
        if actual == status:  # Explicit expected429 and negative cases stay one-shot.
            return result
        wait = int(header) if isinstance(header, str) and re.fullmatch(r'(?:[1-9]|[12][0-9]|30)', header) else 0
        if (eligible and actual == 429 and result.get('code') == 'RATE_LIMITED'
                and wait > 0 and attempt < 6 and wait < deadline - time.monotonic()):
            time.sleep(wait)
            continue
        assert actual == status, (path, actual, status, result)


def login(username, password):
    return call('/session', body={'username': username, 'password': password})['token']


def main():
    admin = login('admin', Path('/run/secrets/admin_password').read_text().strip())
    owner = call('/me', admin)['id']
    for path in ['/admin/users', '/admin/agents', '/admin/events']:
        call(path, status=401)
    password = uuid.uuid4().hex
    payload = {'username': 'researcher-test', 'name': 'Test researcher', 'role': 'researcher', 'password': password}
    key = str(uuid.uuid4())
    user = call('/admin/users', admin, payload, 201, key, retry429=True)
    assert call('/admin/users', admin, payload, 201, key) == user
    call('/admin/users', admin, {**payload, 'name': 'Changed'}, 409, key)
    call('/admin/users', admin, payload, 409)
    call('/admin/users', admin, {**payload, 'username': 'weak-test', 'password': 'short'}, 400)
    token = login(payload['username'], password)
    for path in ['/admin/users', '/admin/agents', '/admin/events']:
        call(path, token, status=403)
    call('/admin/users', token, payload, 403)
    call('/admin/users/' + str(uuid.uuid4()), admin, {'role': 'viewer', 'disabled': False}, 404)
    call('/admin/users/' + owner, admin, {'role': 'viewer', 'disabled': False}, 409)
    call('/admin/users/' + user['id'], admin, {'role': 'viewer', 'disabled': True})
    call('/me', token, status=401)
    call('/session', body={'username': payload['username'], 'password': password}, status=401)
    call('/admin/users/' + user['id'], admin, {'role': 'viewer', 'disabled': False})
    token = login(payload['username'], password)
    call('/projects', token, {'slug': 'forbidden-viewer', 'name': 'forbidden'}, 403)
    call('/account/password', token, {'current_password': 'incorrect', 'password': uuid.uuid4().hex}, 403)
    new_password = uuid.uuid4().hex
    call('/account/password', token, {'current_password': password, 'password': new_password}, retry429=True)
    call('/me', token, status=401)
    token = login(payload['username'], new_password)
    assert call('/me', token)['role'] == 'viewer'
    agents = call('/admin/agents', admin)['items']
    assert len(agents) == 5 and all('identity_fingerprint' not in a for a in agents)
    call('/admin/agents/' + agents[0]['id'], admin, {'disabled': True})
    assert next(a for a in call('/admin/agents', admin)['items'] if a['id'] == agents[0]['id'])['status'] == 'disabled'
    call('/admin/agents/' + agents[0]['id'], admin, {'disabled': False})
    page = call('/admin/users?limit=1', admin)
    assert page['next_cursor']
    call('/admin/events?cursor=' + page['next_cursor'], admin, status=400)
    call('/admin/users?limit=0', admin, status=400)
    events = call('/admin/events', admin)['items']
    assert any(e['action'] == 'password.change' for e in events)
    assert password not in json.dumps(events) and new_password not in json.dumps(events)
    second_password = uuid.uuid4().hex
    second = call('/admin/users', admin, {'username': 'second-admin', 'name': 'Second admin',
        'role': 'admin', 'password': second_password}, 201, retry429=True)
    second_token = login('second-admin', second_password)
    barrier = threading.Barrier(2)

    def demote(pair):
        principal, credential = pair
        barrier.wait(timeout=10)
        try:
            call('/admin/users/' + principal, credential, {'role': 'researcher', 'disabled': False})
            return 'changed'
        except AssertionError as error:
            assert error.args[0][1:3] == (409, 200), error
            assert error.args[0][3]['code'] == 'LAST_ADMIN'
            return 'protected'

    with ThreadPoolExecutor(max_workers=2) as pool:
        assert sorted(pool.map(demote, [(owner, admin), (second['id'], second_token)])) == ['changed', 'protected']
    print('PASS: create/replay/conflict, role authorization, block/unblock, revoked sessions, '
          'password change, agent toggle, cursor scope, safe audit, concurrent last-admin protection')


if __name__ == '__main__':
    main()
