"""Five pooled baselines matching the NIR budget; not federated platform runs."""
import hashlib
import json
from pathlib import Path
import torch

from fl_client.core.model import configure, create, encode
from fl_client.core.trainer import train
from fl_client.core.evaluation import evaluate
from fl_client.datasets.digits import dataset, digest_json


def main():
    configure()
    output = Path('/evidence')
    results = []
    for seed in [11, 22, 33, 44, 55]:
        features, labels, validation, test, _, split, _ = dataset(seed, 'balanced')
        indices = split['train']
        x, y = torch.from_numpy(features), torch.from_numpy(labels)
        model = create(seed)
        initial_hash = hashlib.sha256(encode(model)).hexdigest()
        history = []
        for round_no in range(1, 26):
            train(model, x[indices], y[indices], algorithm='fedavg', mu=0,
                  epochs=3, batch_size=25, learning_rate=.12,
                  seed=seed, round_no=round_no, client_index=0)
            history.append({'round': round_no, 'validation': evaluate(model, x[validation], labels[validation])})
        result = {'method': 'central', 'seed': seed, 'rounds': 25, 'epochs_per_round': 3,
            'train_samples': 1000, 'validation_samples': 250, 'test_samples': 547,
            'dataset_manifest_hash': digest_json(split), 'initial_weights_hash': initial_hash,
            'final_weights_hash': hashlib.sha256(encode(model)).hexdigest(),
            'history': history, 'test': evaluate(model, x[test], labels[test])}
        results.append(result)
        (output / f'central-{seed}.json').write_text(json.dumps(result))
        print('central', seed, result['test']['accuracy'], flush=True)
    (output / 'central.json').write_text(json.dumps(results, indent=2))
    print('PASS: five central baselines, 75 epochs each, fixed split, final test only')


if __name__ == '__main__':
    main()
