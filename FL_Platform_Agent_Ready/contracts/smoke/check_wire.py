"""Independent Python contract oracle; no application or storage implementation."""
import copy
import hashlib
import json
import math
from pathlib import Path
import re
import struct
import sys

from google.protobuf import descriptor_pb2
import fl_platform_pb2 as pb
import fl_platform_pb2_grpc  # Import generated service stubs as part of smoke.

MAX_BYTES = 256 * 1024 * 1024


def schema_bytes(schema):
    if schema.schema_version != 1 or not 1 <= len(schema.tensors) <= 4096:
        raise ValueError('schema version/count')
    names, total, lines = set(), 0, ['FLMODEL1\n']
    for tensor in schema.tensors:
        if (not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_.]{0,127}', tensor.name)
                or tensor.name in names or tensor.dtype != 'FLOAT32_LE'
                or len(tensor.shape) > 8):
            raise ValueError('tensor metadata')
        names.add(tensor.name)
        numel = 1
        for dim in tensor.shape:
            if not 1 <= dim <= 2147483647 or numel > MAX_BYTES // 4 // dim:
                raise ValueError('dimension/overflow')
            numel *= dim
        total += numel
        if tensor.numel != numel or total > MAX_BYTES // 4:
            raise ValueError('numel/limit')
        shape = ','.join(map(str, tensor.shape)) or '-'
        lines.append(f'{tensor.name}\tFLOAT32_LE\t{shape}\t{numel}\n')
    return ''.join(lines).encode('ascii')


def validate(schema, chunks):
    digest = hashlib.sha256(schema_bytes(schema)).hexdigest()
    expected = sum(t.numel for t in schema.tensors) * 4
    if not chunks:
        raise ValueError('empty stream')
    offset, payload, metadata = 0, bytearray(), None
    for chunk in chunks:
        current = (chunk.upload_id, chunk.expected_sha256_hex, chunk.codec,
                   chunk.model_schema_hash, chunk.total_size_bytes,
                   chunk.task_id, chunk.lease_id, chunk.lease_token)
        if metadata is not None and current != metadata:
            raise ValueError('changed metadata')
        metadata = current
        if (chunk.codec != 'FLOAT32_LE' or chunk.model_schema_hash != digest
                or chunk.total_size_bytes != expected or chunk.offset != offset
                or not 0 < len(chunk.data) <= 1024 * 1024 or len(chunk.data) % 4
                or offset + len(chunk.data) > expected):
            raise ValueError('chunk boundary/schema')
        payload.extend(chunk.data)
        offset += len(chunk.data)
    if offset != expected or hashlib.sha256(payload).hexdigest() != chunks[0].expected_sha256_hex:
        raise ValueError('truncated/checksum')
    if not all(math.isfinite(x[0]) for x in struct.iter_unpack('<f', payload)):
        raise ValueError('non-finite')
    return bytes(payload)


def compatibility():
    desc = descriptor_pb2.FileDescriptorProto.FromString(pb.DESCRIPTOR.serialized_pb)
    messages = {m.name: m for m in desc.message_type}
    baseline = json.loads(Path('/src/contracts/smoke/baseline.json').read_text())
    for name, fields in baseline.items():
        actual = {f.number: f for f in messages[name].field}
        for field, number, kind, repeated in fields:
            f = actual[number]
            typ = (f.type_name.removeprefix('.flplatform.v1.').lstrip('.') if f.type_name
                   else descriptor_pb2.FieldDescriptorProto.Type.Name(f.type)[5:].lower())
            assert (f.name, typ, f.label == 3) == (field, kind, repeated), (name, field)
    services = {s.name: s for s in desc.service}
    for method in ('Register', 'Heartbeat', 'AcquireTask', 'SubmitUpdate', 'ReportFailure'):
        m = next(m for m in services['ParticipantService'].method if m.name == method)
        assert m.input_type == '.flplatform.v1.' + method + 'Request'
        assert m.output_type == '.flplatform.v1.' + method + 'Response'
        assert not m.client_streaming and not m.server_streaming
    up, down = services['ArtifactService'].method
    assert (up.name, up.input_type, up.output_type, up.client_streaming, up.server_streaming) == (
        'Upload', '.flplatform.v1.ArtifactChunk', '.flplatform.v1.UploadReceipt', True, False)
    assert (down.name, down.input_type, down.output_type, down.client_streaming, down.server_streaming) == (
        'Download', '.flplatform.v1.DownloadRequest', '.flplatform.v1.ArtifactChunk', False, True)
    print(f'PASS: original fields in {len(baseline)} messages and 7 RPC signatures')


def expect_reject(schema, chunks):
    try:
        validate(schema, chunks)
    except ValueError:
        return
    raise AssertionError('invalid fixture accepted')


def prepare():
    compatibility()
    schema = pb.ModelSchema(schema_version=1, tensors=[
        pb.TensorSpec(name='dense.weight', dtype='FLOAT32_LE', shape=[1, 3], numel=3),
        pb.TensorSpec(name='dense.bias', dtype='FLOAT32_LE', numel=1)])
    payload = struct.pack('<4f', 1.0, -2.5, 0.0, 3.25)
    canonical = schema_bytes(schema)
    digest = hashlib.sha256(canonical).hexdigest()
    chunk = pb.ArtifactChunk(upload_id='fixture-upload', offset=0, data=payload,
        expected_sha256_hex=hashlib.sha256(payload).hexdigest(), codec='FLOAT32_LE',
        model_schema_hash=digest, total_size_bytes=16, task_id='fixture-task',
        lease_id='fixture-lease', lease_token='synthetic-test-token')
    assert validate(schema, [chunk]) == payload
    first, last = copy.deepcopy(chunk), copy.deepcopy(chunk)
    first.data, last.data, last.offset = payload[:8], payload[8:], 8
    assert validate(schema, [first, last]) == payload
    cases = 0
    for attr, value in [('offset', 4), ('total_size_bytes', MAX_BYTES + 4),
                        ('model_schema_hash', '0' * 64), ('codec', 'FLOAT32_BE'),
                        ('data', b''), ('data', b'bad'), ('data', payload[:-4]),
                        ('expected_sha256_hex', '0' * 64)]:
        broken = copy.deepcopy(chunk)
        setattr(broken, attr, value)
        expect_reject(schema, [broken])
        cases += 1
    for value in (math.nan, math.inf, -math.inf):
        broken = copy.deepcopy(chunk)
        broken.data = struct.pack('<4f', value, 1, 2, 3)
        broken.expected_sha256_hex = hashlib.sha256(broken.data).hexdigest()
        expect_reject(schema, [broken])
        cases += 1
    expect_reject(schema, [last, first])
    changed = copy.deepcopy(last)
    changed.lease_id = 'another-lease'
    expect_reject(schema, [first, changed])
    for mutation in ('duplicate', 'numel', 'zero', 'overflow', 'order'):
        broken = copy.deepcopy(schema)
        if mutation == 'duplicate': broken.tensors[1].name = broken.tensors[0].name
        if mutation == 'numel': broken.tensors[0].numel = 2
        if mutation == 'zero': broken.tensors[0].shape[0] = 0
        if mutation == 'overflow': broken.tensors[0].shape[:] = [2147483647, 2147483647]
        if mutation == 'order': broken.tensors.reverse()
        expect_reject(broken, [chunk])
    Path('schema.sha').write_text(digest)
    Path('schema.bin').write_bytes(schema.SerializeToString())
    Path('python.bin').write_bytes(chunk.SerializeToString())
    assignment = pb.TaskAssignment(task_id='fixture-task', attempt=1, model_schema=schema,
        global_model=pb.ArtifactRef(schema_version=1, model_schema_hash=digest))
    Path('assignment.bin').write_bytes(assignment.SerializeToString())
    print(f'PASS: Python schema/chunks, {cases + 7} negative cases')


def verify():
    schema = pb.ModelSchema.FromString(Path('schema.bin').read_bytes())
    chunk = pb.ArtifactChunk.FromString(Path('go.bin').read_bytes())
    assert validate(schema, [chunk]) == struct.pack('<4f', 1.0, -2.5, 0.0, 3.25)
    assert not chunk.lease_token and not chunk.lease_id and not chunk.task_id and not chunk.upload_id
    response = pb.SubmitUpdateResponse.FromString(Path('response.bin').read_bytes())
    assert response.accepted and response.update_id == 'from-go' and response.status_code == 'ACCEPTED'
    print('PASS: Go -> Python chunks and SubmitUpdateResponse')


if __name__ == '__main__':
    {'prepare': prepare, 'verify': verify}[sys.argv[1]]()
