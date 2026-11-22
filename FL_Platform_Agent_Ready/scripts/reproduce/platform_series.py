"""Resumable 30-run protocol through the real API; run on the control VPS."""
import hashlib
import csv
import io
import json
from pathlib import Path
import statistics
import sys
import time
import uuid
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'tests/system'))
import mvp_smoke as api

OUT = Path('/opt/fl-platform/research-release')


def save(value):
    temporary = OUT / 'series.tmp'
    temporary.write_text(json.dumps(value, indent=2))
    temporary.replace(OUT / 'series.json')


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    api.TOKEN = api.request('/session', 'POST', {'username': 'admin',
        'password': Path('/opt/fl-platform/secrets/admin_password').read_text().strip()})['token']
    state_path = OUT / 'series.json'
    if state_path.exists():
        state = json.loads(state_path.read_text())
    else:
        project = api.request('/projects', 'POST', {'slug': 'vkr-' + uuid.uuid4().hex[:8],
            'name': 'ВКР · серия 30 запусков · 5 seed'}, expected=201)
        state = {'project_id': project['id'], 'runs': []}
        save(state)
    presets = {p['id']: p['config'] for p in api.request('/presets')['items']}
    for seed in [11, 22, 33, 44, 55]:
        for partition in ['balanced', 'moderate', 'strong']:
            for algorithm, mu in [('fedavg', 0), ('fedprox', .1)]:
                label = f'{algorithm}-{partition}-{seed}'
                if any(r['label'] == label for r in state['runs']):
                    continue
                config = {**presets[f'digits-{partition}-{seed}'], 'algorithm': algorithm, 'mu': mu}
                key = str(uuid.uuid5(uuid.UUID(state['project_id']), label))
                experiment = api.request(f"/projects/{state['project_id']}/experiments", 'POST',
                    {'name': label, 'config': config}, key=key, expected=201)
                state['runs'].append({'label': label, 'algorithm': algorithm, 'partition': partition,
                    'seed': seed, 'experiment_id': experiment['id'], 'key': key})
                save(state)
    # Bound resource pressure while keeping five independently executed clients.
    for start in range(0, 30, 5):
        batch = state['runs'][start:start + 5]
        for item in batch:
            if 'id' not in item:
                run = api.request(f"/experiments/{item['experiment_id']}/runs", 'POST',
                                  key=item['key'], expected=202)
                item['id'] = run['id']
                save(state)
        deadline = time.monotonic() + 1800
        while True:
            states = [api.request(f"/runs/{item['id']}") for item in batch]
            assert all(r['status'] not in {'failed', 'cancelled'} for r in states), states
            print('batch', start // 5 + 1, [(r['status'], r['completed_rounds']) for r in states], flush=True)
            if all(r['status'] == 'completed' for r in states):
                break
            assert time.monotonic() < deadline, 'batch timeout; state saved for inspection'
            time.sleep(10)
        for item in batch:
            payload = api.request(f"/runs/{item['id']}/export")
            (OUT / (item['label'] + '.zip')).write_bytes(payload)
            with zipfile.ZipFile(io.BytesIO(payload)) as archive:
                metrics = json.loads(archive.read('metrics.json'))
                models = json.loads(archive.read('models.json'))
                assert hashlib.sha256(archive.read("final_model.bin")).hexdigest() == models[-1]["sha256"]
                assert len(models) == 26 and len(metrics) == 78
                item['model_sha256'] = models[-1]['sha256']
                item['test'] = {m['name']: m['value'] for m in metrics if m['split'] == 'test_global'}
        save(state)
    rows = []
    for partition in ['balanced', 'moderate', 'strong']:
        for algorithm in ['fedavg', 'fedprox']:
            group = [r for r in state['runs'] if r['partition'] == partition and r['algorithm'] == algorithm]
            assert len(group) == 5
            row = {'partition': partition, 'algorithm': algorithm, 'seeds': 5}
            for metric in ['accuracy', 'macro_f1', 'cross_entropy']:
                values = [r['test'][metric] for r in group]
                row[metric + '_mean'] = statistics.mean(values)
                row[metric + '_sample_sd'] = statistics.stdev(values)
            rows.append(row)
    with (OUT / 'summary.csv').open('w', newline='') as output:
        writer = csv.DictWriter(output, fieldnames=list(rows[0]))
        writer.writeheader(); writer.writerows(rows)
    print('PASS: 30 real 25-round runs, five seeds, three partitions, paired methods; summary.csv', flush=True)


if __name__ == '__main__':
    main()
