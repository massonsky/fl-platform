"""Negative project authorization using disposable user/session fixtures."""
import hashlib
import json
from pathlib import Path
import secrets
import subprocess
import uuid

import mvp_smoke as api


def sql(statement):
    result = subprocess.run(["docker", "compose", "exec", "-T", "postgres", "psql",
                             "-U", "flrp", "-d", "flrp", "-v", "ON_ERROR_STOP=1", "-At"],
                            input=statement, text=True, capture_output=True, check=True)
    return result.stdout.strip()


def main():
    evidence = json.loads(Path("/opt/fl-platform/mvp-checks/runs.json").read_text())
    foreign = evidence["runs"][0]
    user, project = str(uuid.uuid4()), str(uuid.uuid4())
    token = secrets.token_hex(32)
    token_hash = hashlib.sha256(token.encode()).hexdigest()
    # All SQL interpolation is generated UUID/hex, never a user-supplied value.
    sql(f"""BEGIN;
      INSERT INTO users(id,external_subject,display_name,role)
      VALUES('{user}','access-test-{user}','Disposable viewer','viewer');
      INSERT INTO projects(id,owner_id,slug,name)
      VALUES('{project}','{user}','access-{user}','Disposable project');
      INSERT INTO sessions(token_hash,user_id,expires_at)
      VALUES('{token_hash}','{user}',now()+interval '10 minutes'); COMMIT;""")
    api.TOKEN = token
    try:
        assert api.request("/me")["role"] == "viewer"
        projects = api.request("/projects")["items"]
        assert len(projects) == 1 and projects[0]["id"] == project
        preset = api.request("/presets")["items"][0]["config"]
        api.request(f"/projects/{project}/experiments", "POST",
                    {"name": "Forbidden", "config": preset}, expected=403)
        api.request(f"/projects/{evidence['project_id']}/experiments", expected=404)
        for suffix in ["", "/metrics", "/rounds", "/events", "/export"]:
            api.request(f"/runs/{foreign['id']}{suffix}", expected=404)
        api.request(f"/runs/{foreign['id']}/cancel", "POST", expected=404)
        api.request(f"/experiments/{foreign['experiment_id']}/runs", "POST", expected=404)
        api.request("/session", "DELETE")
        api.request("/me", expected=401)
        assert sql(f"SELECT count(*) FROM experiments WHERE project_id='{project}';") == "0"
    finally:
        sql(f"BEGIN; DELETE FROM sessions WHERE user_id='{user}'; DELETE FROM projects WHERE id='{project}'; DELETE FROM users WHERE id='{user}'; COMMIT;")
    print("PASS: owner-only reads, viewer mutation denial, foreign resources hidden, session revocation")


if __name__ == "__main__":
    main()
