"""VPS-only SigV4 smoke check, using stdlib and local secret files."""
import argparse
import datetime
import hashlib
import hmac
import os
from pathlib import Path
import urllib.error
import urllib.request

BASE = 'http://127.0.0.1:19000'
HOST = '127.0.0.1:19000'
BUCKET = '/flrp-artifacts'
KEY = BUCKET + '/infra-smoke/persistence.txt'
CONTENT = b'FL Platform infrastructure persistence probe v1\n'
ROOT = Path(os.environ.get('FL_SECRETS_DIR', '/opt/fl-platform/secrets'))


def request(method, path, payload=b'', query='', anonymous=False, invalid=False):
    now = datetime.datetime.now(datetime.timezone.utc)
    stamp, day = now.strftime('%Y%m%dT%H%M%SZ'), now.strftime('%Y%m%d')
    digest = hashlib.sha256(payload).hexdigest()
    headers = {'Host': HOST, 'x-amz-date': stamp, 'x-amz-content-sha256': digest}
    signed = 'host;x-amz-content-sha256;x-amz-date'
    canonical_headers = f'host:{HOST}\nx-amz-content-sha256:{digest}\nx-amz-date:{stamp}\n'
    canonical = '\n'.join((method, path, query, canonical_headers, signed, digest))
    scope = f'{day}/us-east-1/s3/aws4_request'
    to_sign = '\n'.join(('AWS4-HMAC-SHA256', stamp, scope,
                         hashlib.sha256(canonical.encode()).hexdigest()))
    secret = ROOT.joinpath('minio_password').read_text().strip()
    key = ('AWS4' + secret).encode()
    for value in (day, 'us-east-1', 's3', 'aws4_request'):
        key = hmac.new(key, value.encode(), hashlib.sha256).digest()
    signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()
    if invalid:
        signature = '0' * 64
    if not anonymous:
        user = ROOT.joinpath('minio_user').read_text().strip()
        headers['Authorization'] = (
            f'AWS4-HMAC-SHA256 Credential={user}/{scope}, '
            f'SignedHeaders={signed}, Signature={signature}')
    url = BASE + path + ('?' + query if query else '')
    req = urllib.request.Request(url, data=payload if method == 'PUT' else None,
                                 method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--verify-only', action='store_true')
    args = parser.parse_args()
    if not args.verify_only:
        status, _ = request('HEAD', BUCKET)
        if status == 404:
            assert request('PUT', BUCKET)[0] == 200, 'Bucket creation failed'
        else:
            assert status == 200, 'Bucket inaccessible'
        versioning = (b'<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">'
                      b'<Status>Enabled</Status></VersioningConfiguration>')
        assert request('PUT', BUCKET, versioning, query='versioning=')[0] == 200
        assert request('PUT', KEY, CONTENT)[0] == 200, 'Object upload failed'
    status, body = request('GET', KEY)
    assert status == 200 and body == CONTENT, 'Persistence/checksum mismatch'
    assert request('GET', KEY, anonymous=True)[0] == 403, 'Anonymous access not denied'
    assert request('GET', KEY, invalid=True)[0] == 403, 'Invalid signature not denied'
    status, body = request('GET', BUCKET, query='versioning=')
    assert status == 200 and b'<Status>Enabled</Status>' in body, 'Versioning disabled'
    print('PASS: S3 signed read/write, checksum, versioning, anonymous and bad-signature denial')


if __name__ == '__main__':
    main()
