import copy
import json
from pathlib import Path
import unittest

import numpy as np
import torch

from fl_client.core import model
from fl_client.core.trainer import proximal_penalty, train
from fl_client.datasets.digits import dataset, partition_indices


class ScienceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        model.configure()

    def test_all_original_indices_and_class_quotas(self):
        fixture = Path(__file__).with_name("fixtures") / "nir_splits.json"
        original = json.loads(fixture.read_text(encoding="utf-8"))
        for seed in [11, 22, 33, 44, 55]:
            for mode, base in [("balanced", 20), ("moderate", 10), ("strong", 2)]:
                _, labels, _, _, parts, splits, _ = dataset(seed, mode)
                for split in ["train", "validation", "test"]:
                    self.assertEqual(splits[split], original[split])
                self.assertEqual([p.tolist() for p in parts],
                                 original["clients"][f"{seed}_{mode}"])
                for client, part in enumerate(parts):
                    expected = [100 - 4 * base if c // 2 == client else base
                                for c in range(10)]
                    self.assertEqual(np.bincount(labels[part], minlength=10).tolist(),
                                     expected)
                self.assertFalse(set(splits["train"]) & set(splits["test"]))
                self.assertFalse(set(splits["validation"]) & set(splits["test"]))

    def test_partition_rejects_unknown_mode_and_missing_samples(self):
        with self.assertRaises(ValueError):
            partition_indices(np.array([]), np.array([]), 11, "unknown")
        with self.assertRaises(ValueError):
            partition_indices(np.array([], dtype=int), np.array([]), 11, "strong")

    def test_proximal_known_answer_and_frozen_reference(self):
        parameter = torch.tensor([3.0], requires_grad=True)
        reference = torch.tensor([1.0])
        penalty = proximal_penalty([parameter], [reference], 0.1)
        penalty.backward()
        self.assertAlmostEqual(penalty.item(), 0.2)
        self.assertAlmostEqual(parameter.grad.item(), 0.2)
        self.assertEqual(reference.tolist(), [1.0])
        self.assertIsNone(reference.grad)

    def test_zero_mu_equivalence_and_nonzero_mu_effect(self):
        x, y, _, _, parts, _, _ = dataset(11, "strong")
        original = model.create(11)
        models = [copy.deepcopy(original) for _ in range(3)]
        options = dict(epochs=3, batch_size=25, learning_rate=0.12,
                       seed=11, round_no=1, client_index=0)
        for candidate, algorithm, mu in zip(models, ["fedavg", "fedprox", "fedprox"],
                                            [0.0, 0.0, 0.1]):
            train(candidate, torch.from_numpy(x[parts[0]]),
                  torch.from_numpy(y[parts[0]]), algorithm=algorithm, mu=mu, **options)
        self.assertEqual(model.encode(models[0]), model.encode(models[1]))
        self.assertNotEqual(model.encode(models[0]), model.encode(models[2]))
        self.assertEqual(model.encode(original), model.encode(model.create(11)))

    def test_weight_validation_and_roundtrip(self):
        original = model.create(22)
        payload = model.encode(original)
        target = model.create(55)
        model.decode(target, payload)
        self.assertEqual(model.encode(target), payload)
        self.assertEqual(model.schema(original), model.schema(target))
        for malformed in [payload[:-1], b"", np.full(len(payload) // 4,
                                                     np.nan, dtype="<f4").tobytes()]:
            with self.assertRaises(ValueError):
                model.decode(target, malformed)


if __name__ == "__main__":
    unittest.main()
