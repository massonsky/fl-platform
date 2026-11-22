"""Independently verify exported evidence, pairing, confusion metrics and summary."""
import csv
import hashlib
import json
import math
from pathlib import Path
import statistics
import sys
import zipfile


def close(actual, expected):
    assert math.isclose(actual, expected, rel_tol=0, abs_tol=1e-12), (actual, expected)


def confusion_metrics(flat, count):
    assert len(flat) == 100 and all(isinstance(x, int) and x >= 0 for x in flat)
    assert sum(flat) == count
    accuracy = sum(flat[c * 10 + c] for c in range(10)) / count
    scores = []
    for c in range(10):
        denominator = sum(flat[c * 10:(c + 1) * 10]) + sum(flat[c::10])
        scores.append(2 * flat[c * 10 + c] / denominator if denominator else 0)
    return {'accuracy': accuracy, 'macro_f1': statistics.mean(scores)}


def main(directory):
    state = json.loads((directory / 'series.json').read_text())
    runs = state['runs']
    assert len(runs) == len({r['id'] for r in runs}) == 30
    configs = {}
    for run in runs:
        with zipfile.ZipFile(directory / (run['label'] + '.zip')) as archive:
            config = json.loads(archive.read('config.json'))
            manifest = json.loads(archive.read('run_manifest.json'))
            models = json.loads(archive.read('models.json'))
            metrics = json.loads(archive.read('metrics.json'))
            confusions = json.loads(archive.read('confusions.json'))
            assert manifest['run_id'] == run['id'] and manifest['config'] == config
            assert len(models) == 26 and len(metrics) == 78 and len(confusions) == 25
            assert hashlib.sha256(archive.read('final_model.bin')).hexdigest() == run['model_sha256'] == models[-1]['sha256']
            assert all(c['test'] is None for c in confusions[:-1])
            measured = confusion_metrics(confusions[-1]['test'], 547)
            exported = {m['name']: m['value'] for m in metrics if m['split'] == 'test_global'}
            assert exported == run['test']
            for name, value in measured.items():
                close(value, exported[name])
            assert config['rounds'] == 25 and config['clients'] == 5
            assert config['completion_policy'] == {'mode': 'strict'}
            configs[(run['seed'], run['partition'], run['algorithm'])] = config
    central = json.loads((directory / 'central.json').read_text())
    assert {c['seed'] for c in central} == {11, 22, 33, 44, 55} and len(central) == 5
    for baseline in central:
        for partition in ['balanced', 'moderate', 'strong']:
            pair = [dict(configs[(baseline['seed'], partition, a)]) for a in ['fedavg', 'fedprox']]
            for config in pair:
                algorithm = config.pop('algorithm')
                assert config.pop('mu') == (0 if algorithm == 'fedavg' else .1)
                for key in ['dataset_manifest_hash', 'initial_weights_hash']:
                    assert config[key] == baseline[key], key
            assert pair[0] == pair[1], 'paired methods must differ only by algorithm and mu'
    with (directory / 'summary.csv').open() as source:
        rows = list(csv.DictReader(source))
    assert len(rows) == 6
    for row in rows:
        group = [r for r in runs if r['algorithm'] == row['algorithm'] and r['partition'] == row['partition']]
        assert len(group) == 5
        for metric in ['accuracy', 'macro_f1', 'cross_entropy']:
            values = [r['test'][metric] for r in group]
            close(float(row[metric + '_mean']), statistics.mean(values))
            close(float(row[metric + '_sample_sd']), statistics.stdev(values))
    print('PASS: 30 exports, final weights SHA, 15 matched pairs, five central baselines, confusion-derived accuracy/F1, mean/sample SD')


if __name__ == '__main__':
    main(Path(sys.argv[1]))
