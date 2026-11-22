import hashlib
import uuid

import fl_platform_pb2 as pb


def download(stub, assignment, metadata):
    ref = assignment.global_model
    request = pb.DownloadRequest(artifact_id=ref.artifact_id,
                                 expected_sha256_hex=ref.sha256_hex,
                                 task_id=assignment.task_id, lease_id=assignment.lease_id,
                                 lease_token=assignment.lease_token)
    result = bytearray()
    for chunk in stub.Download(request, metadata=metadata, timeout=30):
        if (chunk.offset != len(result) or len(chunk.data) > 1048576
                or chunk.total_size_bytes != ref.size_bytes
                or chunk.expected_sha256_hex != ref.sha256_hex
                or chunk.model_schema_hash != ref.model_schema_hash
                or chunk.codec != "FLOAT32_LE" or len(result) + len(chunk.data) > 9640):
            raise ValueError("invalid artifact chunk")
        result.extend(chunk.data)
    if len(result) != ref.size_bytes or hashlib.sha256(result).hexdigest() != ref.sha256_hex:
        raise ValueError("artifact hash mismatch")
    return bytes(result)


def upload(stub, assignment, payload, metadata):
    chunk = pb.ArtifactChunk(upload_id=str(uuid.uuid5(uuid.NAMESPACE_URL, assignment.task_id)),
                             offset=0, data=payload,
                             expected_sha256_hex=hashlib.sha256(payload).hexdigest(),
                             codec="FLOAT32_LE", model_schema_hash=assignment.global_model.model_schema_hash,
                             total_size_bytes=len(payload), task_id=assignment.task_id,
                             lease_id=assignment.lease_id, lease_token=assignment.lease_token)
    return stub.Upload(iter([chunk]), metadata=metadata, timeout=30).artifact
