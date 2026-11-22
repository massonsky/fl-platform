"""Offline simulation provisioning; writes only manifests and initial weights."""
import hashlib
import json
from pathlib import Path
import platform
import sys

import grpc
import numpy as np
import sklearn
import torch

from fl_client.core import model
from fl_client.datasets.digits import dataset, digest_json


def provision(destination):
    destination.mkdir(parents=True, exist_ok=True)
    model.configure()
    runtime = {"python": platform.python_version(), "torch": torch.__version__,
               "numpy": np.__version__, "sklearn": sklearn.__version__,
               "grpc": grpc.__version__, "device": "cpu", "threads": 1,
               "trainer_revision": "nir-sgd-v1", "deterministic": True}
    presets = []
    for seed in [11, 22, 33, 44, 55]:
        initial = model.create(seed)
        payload = model.encode(initial)
        specs, schema_hash = model.schema(initial)
        (destination / f"initial-{seed}.bin").write_bytes(payload)
        for mode in ["balanced", "moderate", "strong"]:
            _, _, _, _, _, split, partition = dataset(seed, mode)
            config = {"schema_version": 1, "dataset_id": "digits",
                      "dataset_manifest_hash": digest_json(split),
                      "partition_manifest_hash": digest_json(partition),
                      "model_name": "mlp64-32-10", "model_revision": "nir-v1",
                      "model_schema_hash": schema_hash,
                      "initial_weights_hash": hashlib.sha256(payload).hexdigest(),
                      "runtime_manifest_hash": digest_json(runtime),
                      "algorithm": "fedavg", "clients": 5, "rounds": 25,
                      "seed": seed, "local_epochs": 3, "batch_size": 25,
                      "learning_rate": 0.12, "mu": 0.0, "optimizer": "sgd",
                      "partition": mode, "selection_strategy": "all",
                      "completion_policy": {"mode": "strict"},
                      "round_timeout_seconds": 180, "lease_seconds": 60}
            presets.append({"id": f"digits-{mode}-{seed}", "config": config,
                            "schema": {"schema_version": 1, "tensors": specs},
                            "dataset_manifest": split, "partition_manifest": partition,
                            "runtime_manifest": runtime, "weights_file": f"initial-{seed}.bin"})
    (destination / "presets.json").write_text(json.dumps(presets, allow_nan=False))
    print(f"Provisioned {len(presets)} immutable simulation presets")


if __name__ == "__main__":
    provision(Path(sys.argv[1]))
