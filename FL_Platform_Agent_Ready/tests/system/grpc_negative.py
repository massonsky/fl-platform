"""Run inside the ML image with worker0 temporarily stopped; real TLS RPCs."""
import hashlib
from pathlib import Path
import time
import uuid

import grpc
import numpy as np
import fl_platform_pb2 as pb
import fl_platform_pb2_grpc as rpc

import mvp_smoke as api
from fl_client.transport.artifacts import download, upload


def rejected(call, code):
    try:
        call()
    except grpc.RpcError as error:
        assert error.code() == code, (error.code(), code)
    else:
        raise AssertionError("invalid RPC accepted")


def main():
    api.BASE = "http://api:8080/api/v1"
    password = Path("/run/secrets/admin_password").read_text().strip()
    api.TOKEN = api.request("/session", "POST", {"username": "admin", "password": password})["token"]
    preset = api.request("/presets")["items"][0]["config"]
    project = api.request("/projects", "POST", {"slug": "rpc-" + uuid.uuid4().hex[:10],
                          "name": "Контрактные проверки RPC"}, expected=201)
    experiment = api.request(f"/projects/{project['id']}/experiments", "POST",
                             {"name": "RPC negative test (not a scientific run)",
                              "config": {**preset, "rounds": 1, "lease_seconds": 10}}, expected=201)
    run = api.request(f"/experiments/{experiment['id']}/runs", "POST", expected=202)
    channel = grpc.secure_channel("api:50051", grpc.ssl_channel_credentials(
                                  Path("/run/secrets/server.crt").read_bytes()))
    participant, artifacts = rpc.ParticipantServiceStub(channel), rpc.ArtifactServiceStub(channel)
    metadata = (("authorization", "Bearer " + Path("/run/secrets/client_0").read_text().strip()),)
    registration = pb.RegisterRequest(display_name="simulation-client-0", protocol_version=1,
                                      capabilities=pb.ClientCapabilities(runtime="python-pytorch", sdk_version="0.1.0"))
    rejected(lambda: participant.Register(registration, timeout=10), grpc.StatusCode.UNAUTHENTICATED)
    client = participant.Register(registration, metadata=metadata, timeout=10).client_id
    rejected(lambda: participant.AcquireTask(pb.AcquireTaskRequest(client_id=str(uuid.uuid4())),
                                             metadata=metadata, timeout=10), grpc.StatusCode.PERMISSION_DENIED)
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        response = participant.AcquireTask(pb.AcquireTaskRequest(client_id=client), metadata=metadata, timeout=10)
        if response.has_task:
            task = response.task
            assert task.run_id == run["id"] and task.task_kind == "train"
            break
        time.sleep(1)
    else:
        raise TimeoutError("no assigned task")
    payload = download(artifacts, task, metadata)
    bad = pb.ArtifactChunk(upload_id=str(uuid.uuid4()), offset=0,
                           data=np.full(2410, np.nan, dtype="<f4").tobytes(),
                           codec="FLOAT32_LE", model_schema_hash=task.global_model.model_schema_hash,
                           total_size_bytes=9640, task_id=task.task_id,
                           lease_id=task.lease_id, lease_token=task.lease_token)
    bad.expected_sha256_hex = hashlib.sha256(bad.data).hexdigest()
    rejected(lambda: artifacts.Upload(iter([bad]), metadata=metadata, timeout=10), grpc.StatusCode.INVALID_ARGUMENT)
    bad.data = payload
    rejected(lambda: artifacts.Upload(iter([bad]), metadata=metadata, timeout=10), grpc.StatusCode.INVALID_ARGUMENT)
    foreign = pb.DownloadRequest(artifact_id=str(uuid.uuid4()), expected_sha256_hex=task.global_model.sha256_hex,
                                 task_id=task.task_id, lease_id=task.lease_id, lease_token=task.lease_token)
    rejected(lambda: list(artifacts.Download(foreign, metadata=metadata, timeout=10)), grpc.StatusCode.PERMISSION_DENIED)
    ref = upload(artifacts, task, payload, metadata)
    request = pb.SubmitUpdateRequest(client_id=client, task_id=task.task_id, lease_id=task.lease_id,
                                     lease_token=task.lease_token, idempotency_key=str(uuid.uuid4()),
                                     run_id=task.run_id, round_no=task.round_no,
                                     base_model_version=task.base_model_version,
                                     model_schema_hash=ref.model_schema_hash, update_artifact=ref, sample_count=200)
    for field, value, code in [("base_model_version", 99, grpc.StatusCode.FAILED_PRECONDITION),
                               ("model_schema_hash", "0" * 64, grpc.StatusCode.FAILED_PRECONDITION),
                               ("sample_count", 0, grpc.StatusCode.INVALID_ARGUMENT),
                               ("lease_token", "wrong", grpc.StatusCode.FAILED_PRECONDITION)]:
        modified = pb.SubmitUpdateRequest()
        modified.CopyFrom(request)
        setattr(modified, field, value)
        rejected(lambda: participant.SubmitUpdate(modified, metadata=metadata, timeout=10), code)
    time.sleep(12)
    rejected(lambda: participant.SubmitUpdate(request, metadata=metadata, timeout=10), grpc.StatusCode.FAILED_PRECONDITION)
    replacement = participant.AcquireTask(pb.AcquireTaskRequest(client_id=client), metadata=metadata, timeout=10)
    assert replacement.has_task and replacement.task.attempt == task.attempt + 1
    assert replacement.task.task_id != task.task_id
    task = replacement.task
    payload = download(artifacts, task, metadata)
    ref = upload(artifacts, task, payload, metadata)
    request.task_id, request.lease_id, request.lease_token = task.task_id, task.lease_id, task.lease_token
    request.idempotency_key = str(uuid.uuid4())
    request.update_artifact.CopyFrom(ref)
    first = participant.SubmitUpdate(request, metadata=metadata, timeout=10)
    second = participant.SubmitUpdate(request, metadata=metadata, timeout=10)
    assert first == second and first.accepted
    modified = pb.SubmitUpdateRequest()
    modified.CopyFrom(request)
    modified.sample_count = 201
    rejected(lambda: participant.SubmitUpdate(modified, metadata=metadata, timeout=10), grpc.StatusCode.ALREADY_EXISTS)
    api.request(f"/runs/{run['id']}/cancel", "POST")
    assert participant.SubmitUpdate(request, metadata=metadata, timeout=10) == first
    assert upload(artifacts, task, payload, metadata) == ref
    rejected(lambda: list(artifacts.Download(pb.DownloadRequest(
        artifact_id=task.global_model.artifact_id, expected_sha256_hex=task.global_model.sha256_hex,
        task_id=task.task_id, lease_id=task.lease_id, lease_token=task.lease_token),
        metadata=metadata, timeout=10)), grpc.StatusCode.FAILED_PRECONDITION)
    print("PASS: TLS auth, client identity, artifact scope, NaN/hash, stale/schema/count, lease expiry/reissue, exact replay after cancel")


if __name__ == "__main__":
    main()
