"""Validate real HTTP responses against the resolved OpenAPI schemas."""
import json
from pathlib import Path
import uuid

from openapi_schema_validator import OAS30Validator, oas30_format_checker
from schema_loader import ROOT, load, resolve
import mvp_smoke as api


def main():
    spec = resolve(load("openapi.yaml"), ROOT / "openapi.yaml")
    evidence = json.loads(Path("/evidence/runs.json").read_text())
    api.BASE = "http://api:8080/api/v1"
    password = Path("/run/secrets/admin_password").read_text().strip()
    session = api.request("/session", "POST", {"username": "admin", "password": password})
    api.TOKEN = session["token"]

    def check(template, path, method="get", status=200, body=None):
        value = api.request(path, method.upper(), body, expected=status)
        schema = spec["paths"][template][method]["responses"][str(status)]["content"]["application/json"]["schema"]
        OAS30Validator(schema, format_checker=oas30_format_checker).validate(value)
        return value

    check("/me", "/me")
    check("/presets", "/presets")
    page = check("/projects", "/projects?limit=1")
    if page["next_cursor"]:
        check("/projects", "/projects?limit=1&cursor=" + page["next_cursor"])
    project = evidence["project_id"]
    check("/projects/{projectId}/experiments", f"/projects/{project}/experiments")
    check("/projects/{projectId}/runs", f"/projects/{project}/runs")
    for run in evidence["runs"]:
        check("/experiments/{experimentId}", f"/experiments/{run['experiment_id']}")
        check("/runs/{runId}", f"/runs/{run['id']}")
        for suffix in ["metrics", "rounds", "events"]:
            check("/runs/{runId}/" + suffix, f"/runs/{run['id']}/{suffix}")
        check("/runs/{runId}/cancel", f"/runs/{run['id']}/cancel", "post", 409)
    check("/runs/{runId}", f"/runs/{uuid.uuid4()}", status=404)
    check("/session", "/session", "delete")
    print("PASS: real project/experiment/run/round/metric/event/preset/user/error/cursor/logout response schemas")


if __name__ == "__main__":
    main()
