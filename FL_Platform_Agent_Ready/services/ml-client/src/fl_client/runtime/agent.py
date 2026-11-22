import logging
import os
from pathlib import Path
import random
import threading
import time

import grpc
import fl_platform_pb2 as pb
import fl_platform_pb2_grpc as rpc

from fl_client.core.model import configure
from fl_client.runtime.executor import execute

LOG = logging.getLogger("fl-agent")


def retry(call, request, metadata):
    for attempt in range(5):
        try:
            return call(request, metadata=metadata, timeout=30)
        except grpc.RpcError as error:
            if error.code() not in {grpc.StatusCode.UNAVAILABLE, grpc.StatusCode.DEADLINE_EXCEEDED} or attempt == 4:
                raise
            time.sleep(min(2**attempt, 8) + random.random() * 0.2)


def maintain(participant, client, metadata, current, stop):
    while not stop.wait(10):
        try:
            assignment = current.get("task")
            participant.Heartbeat(pb.HeartbeatRequest(client_id=client,
                                  current_task_id=assignment.task_id if assignment else ""),
                                  metadata=metadata, timeout=10)
            if assignment:
                participant.RenewLease(pb.RenewLeaseRequest(client_id=client,
                                      task_id=assignment.task_id, lease_id=assignment.lease_id,
                                      lease_token=assignment.lease_token), metadata=metadata, timeout=10)
        except grpc.RpcError as error:
            LOG.warning("heartbeat status=%s", error.code().name)


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
    configure()
    shard = int(os.environ["FL_CLIENT_INDEX"])
    token = Path("/run/secrets/client_token").read_text().strip()
    ca = Path("/run/secrets/server.crt").read_bytes()
    metadata = (("authorization", "Bearer " + token),)
    channel = grpc.secure_channel(os.environ.get("FL_GRPC_ADDR", "api:50051"),
                                  grpc.ssl_channel_credentials(ca),
                                  options=[("grpc.max_receive_message_length", 2 << 20)])
    participant, artifacts = rpc.ParticipantServiceStub(channel), rpc.ArtifactServiceStub(channel)
    while True:
        try:
            registered = retry(participant.Register, pb.RegisterRequest(
                display_name=f"simulation-client-{shard}", protocol_version=1,
                capabilities=pb.ClientCapabilities(runtime="python-pytorch", sdk_version="0.1.0",
                                                   model_names=["mlp64-32-10"])), metadata)
            break
        except grpc.RpcError:
            LOG.warning("registration unavailable; retrying")
            time.sleep(5)
    current, stop = {}, threading.Event()
    threading.Thread(target=maintain, args=(participant, registered.client_id, metadata, current, stop),
                     daemon=True).start()
    LOG.info("registered shard=%d", shard)
    while True:
        try:
            response = retry(participant.AcquireTask, pb.AcquireTaskRequest(client_id=registered.client_id), metadata)
            if not response.has_task:
                time.sleep(max(1, response.retry_after_seconds))
                continue
            task = response.task
            current["task"] = task
            call, request = execute(registered.client_id, task, participant, artifacts, metadata)
            retry(call, request, metadata)
            LOG.info("completed run=%s round=%d kind=%s", task.run_id, task.round_no, task.task_kind)
        except grpc.RpcError as error:
            LOG.warning("task RPC status=%s", error.code().name)
            time.sleep(2)
        except Exception as error:
            # Do not log exception text: it may contain local data paths.
            LOG.error("task failed type=%s", type(error).__name__)
            assignment = current.get("task")
            if assignment:
                try:
                    retry(participant.ReportFailure, pb.ReportFailureRequest(
                        client_id=registered.client_id, task_id=assignment.task_id,
                        lease_id=assignment.lease_id, lease_token=assignment.lease_token,
                        idempotency_key=assignment.task_id, failure_code="TRAINING_FAILED"), metadata)
                except grpc.RpcError:
                    pass
            time.sleep(2)
        finally:
            current.pop("task", None)


if __name__ == "__main__":
    main()
