import uuid

import torch
import fl_platform_pb2 as pb

from fl_client.core import model
from fl_client.core.evaluation import evaluate
from fl_client.core.trainer import train
from fl_client.datasets.digits import dataset, digest_json
from fl_client.transport.artifacts import download, upload


def execute(client_id, assignment, participant, artifacts, metadata):
    config = assignment.training
    features, labels, validation, test, parts, _, partition = dataset(config.seed, config.partition)
    if digest_json(partition) != config.partition_manifest_hash:
        raise ValueError("dataset mismatch")
    network = model.create(config.seed)
    specs, schema_hash = model.schema(network)
    received_specs = [{"name": s.name, "dtype": s.dtype, "shape": list(s.shape), "numel": s.numel}
                      for s in assignment.model_schema.tensors]
    if (schema_hash != assignment.global_model.model_schema_hash or specs != received_specs
            or assignment.model_schema.schema_version != 1):
        raise ValueError("model schema mismatch")
    model.decode(network, download(artifacts, assignment, metadata))
    common = dict(client_id=client_id, task_id=assignment.task_id,
                  lease_id=assignment.lease_id, lease_token=assignment.lease_token,
                  idempotency_key=assignment.task_id, run_id=assignment.run_id,
                  round_no=assignment.round_no)
    if assignment.task_kind == "train":
        indices = parts[assignment.client_index]
        count = train(network, torch.from_numpy(features[indices]), torch.from_numpy(labels[indices]),
                      algorithm=config.algorithm, mu=config.mu, epochs=config.local_epochs,
                      batch_size=config.batch_size, learning_rate=config.learning_rate,
                      seed=config.seed, round_no=assignment.round_no,
                      client_index=assignment.client_index)
        ref = upload(artifacts, assignment, model.encode(network), metadata)
        request = pb.SubmitUpdateRequest(**common, base_model_version=assignment.base_model_version,
                                         model_schema_hash=schema_hash, update_artifact=ref,
                                         sample_count=count)
        return participant.SubmitUpdate, request
    if assignment.task_kind != "evaluate":
        raise ValueError("unknown task kind")
    metrics = []
    matrices = {}
    splits = [("validation_global", validation)]
    if assignment.evaluate_test:
        splits.append(("test_global", test))
    for split, indices in splits:
        values = evaluate(network, torch.from_numpy(features[indices]), labels[indices])
        matrices[split] = [v for row in values.pop("confusion_matrix") for v in row]
        metrics.extend(pb.MetricValue(name=name, value=value, split=split)
                       for name, value in values.items())
    request = pb.SubmitEvaluationRequest(**common, model_version=assignment.base_model_version,
                                         metrics=metrics,
                                         validation_confusion=matrices["validation_global"],
                                         test_confusion=matrices.get("test_global", []))
    return participant.SubmitEvaluation, request
