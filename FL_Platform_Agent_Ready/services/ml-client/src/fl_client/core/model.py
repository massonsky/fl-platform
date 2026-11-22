"""Explicit FLOAT32_LE encoding; no pickle or executable deserialization."""
import hashlib

import numpy as np
import torch
from torch import nn


def configure():
    torch.set_num_threads(1)
    torch.use_deterministic_algorithms(True)


def create(seed):
    torch.manual_seed(seed)
    return nn.Sequential(nn.Linear(64, 32), nn.ReLU(), nn.Linear(32, 10))


def schema(model):
    # Stable protocol names preserve the original parameter ordering.
    specs = [{"name": "layer_" + name, "dtype": "FLOAT32_LE",
              "shape": list(value.shape), "numel": value.numel()}
             for name, value in model.named_parameters()]
    canonical = "FLMODEL1\n" + "".join(
        f"{s['name']}\tFLOAT32_LE\t{','.join(map(str, s['shape'])) or '-'}\t{s['numel']}\n"
        for s in specs)
    return specs, hashlib.sha256(canonical.encode("ascii")).hexdigest()


def encode(model):
    weights = np.concatenate([p.detach().cpu().numpy().reshape(-1)
                              for p in model.parameters()]).astype("<f4")
    if not np.isfinite(weights).all():
        raise ValueError("non-finite weights")
    return weights.tobytes()


def decode(model, payload):
    expected = sum(p.numel() for p in model.parameters()) * 4
    if len(payload) != expected:
        raise ValueError("weight size mismatch")
    weights = np.frombuffer(payload, dtype="<f4").copy()
    if not np.isfinite(weights).all():
        raise ValueError("non-finite weights")
    offset = 0
    with torch.no_grad():
        for parameter in model.parameters():
            count = parameter.numel()
            parameter.copy_(torch.from_numpy(weights[offset:offset + count])
                            .reshape(parameter.shape))
            offset += count
