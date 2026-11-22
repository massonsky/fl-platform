#!/usr/bin/env python3
"""Advisory/strict size check for future code, run from repo root after E2."""
import sys
from pathlib import Path
ROOT=Path(sys.argv[1] if len(sys.argv)>1 else '.').resolve()
EXT={'.go','.py','.ts','.tsx','.js','.jsx'}
IGNORE={'node_modules','.venv','venv','dist','build','gen','generated','vendor','__pycache__'}
warn=[]; too_large=[]; n=0
for f in ROOT.rglob('*'):
    if not f.is_file() or f.suffix not in EXT or any(x in IGNORE for x in f.parts):continue
    if f.name.endswith(('.pb.go','_pb2.py','_pb2_grpc.py')):continue
    n+=1
    try: lines=sum(1 for _ in f.open(encoding='utf-8'))
    except (OSError,UnicodeError):continue
    if lines>300:too_large.append((f.relative_to(ROOT),lines))
    elif lines>250:warn.append((f.relative_to(ROOT),lines))
for p,nlines in warn: print(f'[WARN] {p}: {nlines} lines (target <=250)')
for p,nlines in too_large:print(f'[FAIL] {p}: {nlines} lines (>300, split or justify)')
print(f'Scanned {n} code files')
if too_large:sys.exit(1)
