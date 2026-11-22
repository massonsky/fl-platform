"""Real API acceptance, launched on the VPS. Never prints credentials."""
import hashlib
import csv
import io
import json
from pathlib import Path
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
import zipfile

BASE = "http://127.0.0.1:18081/api/v1"
OUT = Path("/opt/fl-platform/mvp-checks")
TOKEN = ""


def request(path, method="GET", value=None, key=None, expected=200, authenticated=True):
    headers = {}
    if authenticated and TOKEN:
        headers["Authorization"] = "Bearer " + TOKEN
    if method == "POST":
        headers["Idempotency-Key"] = key or str(uuid.uuid4())
    data = None
    if value is not None:
        data = json.dumps(value).encode()
        headers["Content-Type"] = "application/json"
    try:
        response = urllib.request.urlopen(urllib.request.Request(BASE + path, data=data,
                                          headers=headers, method=method), timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    payload = response.read()
    if response.code != expected:
        raise AssertionError(f"{method} {path}: expected {expected}, got {response.code}: {payload[:250]!r}")
    if "application/zip" in response.headers.get("Content-Type", ""):
        return payload
    return json.loads(payload)


def main():
    global TOKEN
    OUT.mkdir(parents=True, exist_ok=True)
    previous = {}
    if "--compare-previous" in sys.argv:
        for algorithm in ["fedavg", "fedprox"]:
            with zipfile.ZipFile(OUT / f"{algorithm}.zip") as archive:
                previous[algorithm] = json.loads(archive.read("models.json"))[-1]["sha256"]
    request("/projects", expected=401, authenticated=False)
    password = Path("/opt/fl-platform/secrets/admin_password").read_text().strip()
    TOKEN = request("/session", "POST", {"username": "admin", "password": password})["token"]
    presets = request("/presets")["items"]
    preset = next(p["config"] for p in presets if p["id"] == "digits-balanced-11")
    project_key = str(uuid.uuid4())
    project_input = {"slug": "acceptance-" + uuid.uuid4().hex[:10], "name": "Проверка MVP"}
    project = request("/projects", "POST", project_input, project_key, 201)
    assert project == request("/projects", "POST", project_input, project_key, 201)
    request("/projects", "POST", {**project_input, "name": "Changed"}, project_key, 409)
    request(f"/runs/{uuid.uuid4()}", expected=404)
    request("/projects?cursor=invalid", expected=400)
    bad = {**preset, "algorithm": "fedavg", "mu": 0.1}
    request(f"/projects/{project['id']}/experiments", "POST", {"name": "Invalid", "config": bad}, expected=400)
    runs = []
    for algorithm, mu in [("fedavg", 0), ("fedprox", 0.1)]:
        config = {**preset, "algorithm": algorithm, "mu": mu}
        experiment = request(f"/projects/{project['id']}/experiments", "POST",
                             {"name": algorithm + " · balanced · seed 11", "config": config}, expected=201)
        key = str(uuid.uuid4())
        run = request(f"/experiments/{experiment['id']}/runs", "POST", key=key, expected=202)
        assert run == request(f"/experiments/{experiment['id']}/runs", "POST", key=key, expected=202)
        runs.append({"algorithm": algorithm, "id": run["id"], "experiment_id": experiment["id"]})
    (OUT / "runs.json").write_text(json.dumps({"project_id": project["id"], "runs": runs}))
    deadline, restarted, progress = time.monotonic() + 900, False, {}
    while time.monotonic() < deadline:
        complete = True
        for item in runs:
            run = request(f"/runs/{item['id']}")
            state = (run["status"], run["completed_rounds"])
            if progress.get(item["id"]) != state:
                print(item["algorithm"], item["id"], *state, flush=True)
                progress[item["id"]] = state
            assert run["status"] not in {"failed", "cancelled"}, run
            complete &= run["status"] == "completed"
            if not restarted and 3 <= run["completed_rounds"] < 25:
                subprocess.run(["docker", "compose", "-f", "compose.yaml", "-f", "compose.runtime.yaml",
                                "restart", "orchestrator"], check=True, stdin=subprocess.DEVNULL)
                restarted = True
                print("Restarted orchestrator during active training", flush=True)
        if complete:
            break
        time.sleep(2)
    else:
        raise TimeoutError("MVP runs did not finish")
    assert restarted, "restart scenario was not executed"
    for item in runs:
        run = request(f"/runs/{item['id']}")
        assert run["completed_rounds"] == 25 and run["latest_model_version"] == 25
        request(f"/runs/{item['id']}/cancel", "POST", expected=409)
        payload = request(f"/runs/{item['id']}/export")
        (OUT / f"{item['algorithm']}.zip").write_bytes(payload)
        with zipfile.ZipFile(io.BytesIO(payload)) as archive:
            metrics = json.loads(archive.read("metrics.json"))
            csv_rows = list(csv.DictReader(io.StringIO(archive.read("metrics.csv").decode())))
            assert len(metrics) == len(csv_rows) == 78
            for metric, row in zip(metrics, csv_rows):
                assert metric["value"] == float(row["value"])
            models = json.loads(archive.read("models.json"))
            assert hashlib.sha256(archive.read("final_model.bin")).hexdigest() == models[-1]["sha256"]
            assert len(models) == 26 and all(len(m["update_ids"]) == 5 for m in models[1:])
            if previous:
                assert models[-1]["sha256"] == previous[item["algorithm"]], "repeat model differs"
            confusions = json.loads(archive.read("confusions.json"))
            assert len(confusions) == 25 and sum(confusions[-1]["test"]) == 547
            assert all(c["test"] is None for c in confusions[:-1])
            print(item["algorithm"], "test metrics", [m for m in metrics if m["split"] == "test_global"], flush=True)
    print("PASS: two 25-round runs, replay, negatives, restart, export consistency", flush=True)


if __name__ == "__main__":
    main()
