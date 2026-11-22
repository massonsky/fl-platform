"""Local-only resolver for this acyclic contract's schemas and fixtures."""
from pathlib import Path
from urllib.parse import unquote

import yaml
from openapi_schema_validator import OAS30Validator, oas30_format_checker

ROOT = Path(__file__).resolve().parents[1]


def load(name):
    return yaml.safe_load((ROOT / name).read_text(encoding='utf-8'))


def resolve(value, source):
    if isinstance(value, list):
        return [resolve(v, source) for v in value]
    if not isinstance(value, dict):
        return value
    if '$ref' not in value:
        return {k: resolve(v, source) for k, v in value.items()}
    file, pointer = value['$ref'].split('#', 1)
    target = (source.parent / file).resolve() if file else source
    if not target.is_relative_to(ROOT):
        raise ValueError('external reference outside contract')
    data = yaml.safe_load(target.read_text(encoding='utf-8'))
    for token in pointer.strip('/').split('/'):
        data = data[unquote(token).replace('~1', '/').replace('~0', '~')]
    return resolve(data, target)


def validator(file, name):
    schema = resolve(load(file)[name], ROOT / file)
    return OAS30Validator(schema, format_checker=oas30_format_checker)
