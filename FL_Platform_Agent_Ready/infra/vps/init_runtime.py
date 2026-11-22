"""Provision application-only secrets on the authorized VPS; never print them."""
from pathlib import Path
import os
import re
import secrets
import subprocess


root = Path("/opt/fl-platform/secrets")
root.mkdir(mode=0o700, parents=True, exist_ok=True)
for name in ["admin_password", "session_key", *[f"client_{i}" for i in range(5)]]:
    path = root / name
    if not path.exists():
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(secrets.token_urlsafe(32))
limit_key = root / "auth_limit_key"
if not limit_key.exists():
    descriptor = os.open(limit_key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as output:
        output.write(secrets.token_hex(32))
if not re.fullmatch(r"[0-9a-fA-F]{64}", limit_key.read_text(encoding="utf-8").strip()):
    raise ValueError("Invalid existing auth_limit_key; explicit rotation procedure required")
if not (root / "server.key").exists():
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:3072", "-nodes",
                    "-keyout", str(root / "server.key"), "-out", str(root / "server.crt"),
                    "-days", "365", "-subj", "/CN=fl-platform-api",
                    "-addext", "subjectAltName=DNS:api,DNS:localhost,IP:127.0.0.1"],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    (root / "server.key").chmod(0o600)
print("Application secrets and pinned TLS certificate ready")
