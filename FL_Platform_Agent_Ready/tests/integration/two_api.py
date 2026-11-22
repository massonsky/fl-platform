"""Real TLS concurrency test; exclusively against the isolated test deployment."""
from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import threading
import time
import uuid

import grpc
import fl_platform_pb2 as pb
import fl_platform_pb2_grpc as rpc
from fl_client.transport.artifacts import download, upload
import mvp_smoke as api


def concurrent(call, count=20):
    barrier = threading.Barrier(count)

    def invoke(index):
        barrier.wait(timeout=20)
        return call(index)

    with ThreadPoolExecutor(max_workers=count) as pool:
        return list(pool.map(invoke, range(count)))


def main():
    hosts = [os.environ['TEST_API_A'], os.environ['TEST_API_B']]
    assert all(h.startswith('flrp-check-') for h in hosts)
    api.BASE = f'http://{hosts[0]}:8080/api/v1'
    deadline = time.monotonic() + 30
    while True:
        try:
            password = Path('/run/secrets/admin_password').read_text().strip()
            api.TOKEN = api.request('/session', 'POST', {'username': 'admin', 'password': password})['token']
            break
        except OSError:
            if time.monotonic() > deadline:
                raise
            time.sleep(1)
    channels = [grpc.secure_channel(h + ':50051', grpc.ssl_channel_credentials(
        Path('/run/secrets/server.crt').read_bytes()),
        options=(('grpc.ssl_target_name_override', 'api'),)) for h in hosts]
    participants = [rpc.ParticipantServiceStub(c) for c in channels]
    artifacts = [rpc.ArtifactServiceStub(c) for c in channels]
    clients, credentials = [], []
    for shard in range(5):
        md = (('authorization', 'Bearer ' + Path(f'/run/secrets/client_{shard}').read_text().strip()),)
        request = pb.RegisterRequest(display_name=f'isolated-client-{shard}', protocol_version=1,
            capabilities=pb.ClientCapabilities(runtime='python-pytorch', sdk_version='test'))
        ids = concurrent(lambda i: participants[i % 2].Register(request, metadata=md, timeout=15), 4)
        assert len({r.client_id for r in ids}) == 1, 'duplicate identity across replicas'
        clients.append(ids[0].client_id)
        credentials.append(md)
    preset = api.request('/presets')['items'][0]['config']
    project = api.request('/projects', 'POST', {'slug': 'two-api', 'name': 'Isolated concurrency test'}, expected=201)
    experiment = api.request(f"/projects/{project['id']}/experiments", 'POST',
        {'name': 'Transport fixture, not scientific training', 'config': {**preset, 'rounds': 1, 'lease_seconds': 60}}, expected=201)
    run = api.request(f"/experiments/{experiment['id']}/runs", 'POST', expected=202)
    deadline = time.monotonic() + 30
    while api.request(f"/runs/{run['id']}")['status'] != 'running':
        assert time.monotonic() < deadline, 'run did not start'
        time.sleep(.2)
    time.sleep(1)
    for shard, client in enumerate(clients):
        md = credentials[shard]
        assignments = concurrent(lambda i: participants[i % 2].AcquireTask(
            pb.AcquireTaskRequest(client_id=client), metadata=md, timeout=20))
        winners = [r.task for r in assignments if r.has_task]
        assert len(winners) == 1, f'duplicate assignment for shard {shard}: {len(winners)}'
        task = winners[0]
        assert task.run_id == run['id'] and task.attempt == 1
        payload = download(artifacts[0], task, md)
        ref = upload(artifacts[1], task, payload, md)
        update = pb.SubmitUpdateRequest(client_id=client, task_id=task.task_id,
            lease_id=task.lease_id, lease_token=task.lease_token, idempotency_key=str(uuid.uuid4()),
            run_id=task.run_id, round_no=task.round_no, base_model_version=task.base_model_version,
            model_schema_hash=ref.model_schema_hash, update_artifact=ref, sample_count=200)
        replies = concurrent(lambda i: participants[i % 2].SubmitUpdate(update, metadata=md, timeout=20))
        assert all(r.accepted and r == replies[0] for r in replies), 'non-identical replay'
        altered = pb.SubmitUpdateRequest()
        altered.CopyFrom(update)
        altered.sample_count = 201
        try:
            participants[1].SubmitUpdate(altered, metadata=md, timeout=10)
        except grpc.RpcError as error:
            assert error.code() == grpc.StatusCode.ALREADY_EXISTS
        else:
            raise AssertionError('conflicting payload accepted')
    deadline = time.monotonic() + 30
    while api.request(f"/runs/{run['id']}")['latest_model_version'] != 1:
        assert time.monotonic() < deadline, 'no model publication'
        time.sleep(.2)
    api.BASE = f'http://{hosts[1]}:8080/api/v1'
    api.request(f"/runs/{run['id']}/cancel", 'POST')
    print('PASS: two API processes; 100 concurrent AcquireTask calls -> 5 leases; '
          '100 SubmitUpdate calls -> 5 accepted updates; conflicting replay rejected; '
          'shared registration/session; one model publication')
    for c in channels:
        c.close()


if __name__ == '__main__':
    main()
