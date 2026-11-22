"""Backup application SQL and immutable referenced artifacts, then test restore.

Run from the application root on the VPS. Does not overwrite the live database.
Backups contain account/session hashes and model artifacts: keep directory private.
"""
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import uuid

from check_s3 import BUCKET, request

COMPOSE = ["docker", "compose", "exec", "-T", "postgres"]


def query(database, statement):
    result = subprocess.run(COMPOSE + ["psql", "-U", "flrp", "-d", database,
                                      "-v", "ON_ERROR_STOP=1", "-At", "-c", statement],
                            stdin=subprocess.DEVNULL, capture_output=True, text=True, check=True)
    return result.stdout.strip()


def main():
    os.umask(0o077)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    root = Path("/opt/fl-platform/backups") / (stamp + "-" + uuid.uuid4().hex[:8])
    root.mkdir(parents=True, mode=0o700)
    with (root / "postgres.dump").open("xb") as output:
        subprocess.run(COMPOSE + ["pg_dump", "-U", "flrp", "-Fc", "flrp"],
                       stdin=subprocess.DEVNULL, stdout=output, check=True)
    temporary = "flrp_restore_" + uuid.uuid4().hex
    subprocess.run(COMPOSE + ["createdb", "-U", "flrp", temporary],
                   stdin=subprocess.DEVNULL, check=True)
    try:
        with (root / "postgres.dump").open("rb") as source:
            subprocess.run(COMPOSE + ["pg_restore", "-U", "flrp", "-d", temporary,
                                      "--exit-on-error", "--no-owner"], stdin=source, check=True)
        objects = json.loads(query(temporary, """SELECT coalesce(json_agg(json_build_object(
          'id',id,'key',storage_key,'sha256',sha256_hex,'size',size_bytes)), '[]'::json)
          FROM artifacts WHERE state='committed'"""))
        models = root / "artifacts"
        models.mkdir(mode=0o700)
        for item in objects:
            status, data = request("GET", BUCKET + "/" + item["key"])
            if status != 200 or len(data) != item["size"] or hashlib.sha256(data).hexdigest() != item["sha256"]:
                raise AssertionError("backup artifact integrity failure")
            (models / (str(uuid.UUID(item["id"])) + ".bin")).write_bytes(data)
        (root / "artifacts.json").write_text(json.dumps(objects))
        counts = json.loads(query(temporary, """SELECT json_build_object(
          'runs',(SELECT count(*) FROM runs),'models',(SELECT count(*) FROM model_versions),
          'metrics',(SELECT count(*) FROM metric_points))"""))
        (root / "restore-proof.json").write_text(json.dumps(counts))
        print("PASS: SQL restored into disposable DB;", len(objects), "artifact hashes verified;", counts)
        print("Private backup:", root)
    finally:
        subprocess.run(COMPOSE + ["dropdb", "-U", "flrp", temporary],
                       stdin=subprocess.DEVNULL, check=True)


if __name__ == "__main__":
    main()
