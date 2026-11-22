"""Verify strict failure and deadline-frozen quorum with one unavailable agent."""
import json
from pathlib import Path
import subprocess
import time
import uuid

import mvp_smoke as api

COMPOSE = ["docker", "compose", "-f", "compose.yaml", "-f", "compose.runtime.yaml"]


def main():
    password = Path("/opt/fl-platform/secrets/admin_password").read_text().strip()
    api.TOKEN = api.request("/session", "POST", {"username": "admin", "password": password})["token"]
    preset = api.request("/presets")["items"][0]["config"]
    project = api.request("/projects", "POST", {"slug": "policies-" + uuid.uuid4().hex[:10],
                          "name": "Проверка таймаутов и кворума"}, expected=201)
    subprocess.run(COMPOSE + ["stop", "worker4"], check=True, stdin=subprocess.DEVNULL,
                   stdout=subprocess.DEVNULL)
    try:
        runs = {}
        for mode in ["strict", "quorum"]:
            policy = {"mode": mode}
            if mode == "quorum":
                policy["min_clients"] = 4
            config = {**preset, "rounds": 1, "round_timeout_seconds": 30,
                      "lease_seconds": 10, "completion_policy": policy}
            experiment = api.request(f"/projects/{project['id']}/experiments", "POST",
                                     {"name": mode + " timeout test", "config": config}, expected=201)
            runs[mode] = api.request(f"/experiments/{experiment['id']}/runs", "POST", expected=202)["id"]
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            states = {mode: api.request(f"/runs/{run}") for mode, run in runs.items()}
            if all(r["status"] in {"failed", "completed"} for r in states.values()):
                break
            time.sleep(2)
        else:
            raise TimeoutError("policy tests did not reach terminal states")
        assert states["strict"]["status"] == "failed"
        assert states["strict"]["failure_code"] == "ROUND_TIMEOUT"
        assert states["strict"]["latest_model_version"] == 0
        assert states["quorum"]["status"] == "completed"
        assert states["quorum"]["latest_model_version"] == 1
        rounds = api.request(f"/runs/{runs['quorum']}/rounds")["items"]
        assert len(rounds) == 1 and rounds[0]["accepted_count"] == 4 and rounds[0]["selected_count"] == 5
        Path("/opt/fl-platform/mvp-checks/policies.json").write_text(json.dumps(states))
        print("PASS: missing client fails strict; quorum freezes four at deadline and evaluates once")
    finally:
        subprocess.run(COMPOSE + ["start", "worker4"], check=True, stdin=subprocess.DEVNULL,
                       stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
