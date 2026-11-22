"""Digits simulation: preserve the exact index algorithm from NIR.

The central provisioning step may build partitions in simulation. Distributed
agents must receive only their local partition, never upload samples to Go.
"""
import hashlib
import json

import numpy as np
from sklearn.datasets import load_digits


def digest_json(value):
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"),
                         allow_nan=False).encode()
    return hashlib.sha256(encoded).hexdigest()


def split_indices(labels):
    rng = np.random.default_rng(2026)
    by_class = [rng.permutation(np.where(labels == c)[0]) for c in range(10)]
    return tuple(np.concatenate([a[lower:upper] for a in by_class])
                 for lower, upper in [(0, 100), (100, 125), (125, None)])


def partition_indices(train, labels, seed, mode):
    if mode not in {"balanced", "moderate", "strong"}:
        raise ValueError("unsupported partition")
    if not 0 <= seed <= 2**32 - 1:
        raise ValueError("invalid seed")
    rng = np.random.default_rng(seed)
    base = {"balanced": 20, "moderate": 10, "strong": 2}[mode]
    parts = [[] for _ in range(5)]
    for c in range(10):
        indices = rng.permutation(train[labels[train] == c])
        if len(indices) != 100:
            raise ValueError("training split must contain 100 samples/class")
        start = 0
        for client in range(5):
            count = 100 - 4 * base if client == c // 2 else base
            parts[client].extend(indices[start:start + count])
            start += count
    result = [rng.permutation(part) for part in parts]
    if not all(len(part) == 200 for part in result):
        raise ValueError("invalid partition size")
    if not np.array_equal(np.sort(np.concatenate(result)), np.sort(train)):
        raise ValueError("partition overlap or missing samples")
    return result


def dataset(seed, mode):
    features, labels = load_digits(return_X_y=True)
    features = np.asarray(features / 16, dtype="<f4")
    labels = np.asarray(labels, dtype="<i8")
    train, validation, test = split_indices(labels)
    parts = partition_indices(train, labels, seed, mode)
    split_manifest = {"train": train.tolist(), "validation": validation.tolist(),
                      "test": test.tolist(), "split_revision": "nir-2026-v1",
                      "data_sha256": hashlib.sha256(
                          features.tobytes() + labels.tobytes()).hexdigest()}
    partition_manifest = {"seed": seed, "mode": mode,
                          "clients": [part.tolist() for part in parts]}
    return features, labels, validation, test, parts, split_manifest, partition_manifest
