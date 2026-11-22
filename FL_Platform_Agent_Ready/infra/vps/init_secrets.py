"""Create VPS-only credentials; never overwrite an existing secret."""
import os
from pathlib import Path
import secrets

root = Path(os.environ.get('FL_SECRETS_DIR', '/opt/fl-platform/secrets'))
root.mkdir(parents=True, exist_ok=True, mode=0o700)
os.chmod(root, 0o700)
for name in ('postgres_password', 'minio_user', 'minio_password'):
    path = root / name
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        if path.stat().st_mode & 0o077 or not path.read_text().strip():
            raise SystemExit(f'Unsafe permissions or empty secret: {name}')
        continue
    with os.fdopen(fd, 'w') as stream:
        stream.write(secrets.token_hex(24) + '\n')
print('Secret files ready; values not displayed.')
