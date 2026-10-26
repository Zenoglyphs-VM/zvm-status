"""Synthetic text-only artifact transfer controls; no build or deployment."""
import argparse
import base64
import binascii
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

import pages_gate as gate

FIXTURE_LIMIT = 32 << 10
ENVIRONMENT = {'VITE_ZVM_NODE': 'https://node.example.invalid/zvm',
    'VITE_GATEWAY': 'https://gateway.example.invalid',
    'VITE_NOM_NODE': 'wss://nom.example.invalid/zenon'}
EVIDENCE = {key: gate.digest(('synthetic-pages-handoff-' + key).encode())
    for key in ['chainEpoch', 'releaseManifestSha256', 'qualificationEvidenceSha256']}

def record_bytes(source, core, repository):
    now = datetime.now(timezone.utc).replace(microsecond=0)
    stamp = lambda seconds: (now + timedelta(seconds=seconds)).strftime('%Y-%m-%dT%H:%M:%SZ')
    return gate.encode({'schema': 1, 'repository': repository,
        'sourceCommit': source, 'coreCommit': core,
        'basePath': '/' + repository.split('/')[-1] + '/', 'chainId': '1',
        **EVIDENCE, 'validFrom': stamp(-60), 'validUntil': stamp(3600),
        'environment': ENVIRONMENT, 'scope': gate.SCOPE})

def selected_record(encoded, approved):
    gate.require(type(encoded) is str and 0 < len(encoded) <= (gate.RECORD_LIMIT + 2) // 3 * 4,
        'Synthetic record encoding exceeds its bound')
    try:
        raw = base64.b64decode(encoded, validate=True)
    except (ValueError, binascii.Error) as error:
        raise gate.Refused('Invalid synthetic record encoding') from error
    gate.require(gate.hex_value(approved, 64) and gate.digest(raw) == approved,
        'Trusted synthetic record digest differs')
    record = gate.decode(raw, gate.RECORD_LIMIT)
    gate.require(type(record) is dict and record.get('environment') == ENVIRONMENT
        and record.get('chainId') == '1'
        and all(record.get(key) == value for key, value in EVIDENCE.items()),
        'Only synthetic fixture configuration is supported')
    return raw, record

def production_cli(operation, root, raw, source, core, repository,
                   manifest='', expected_pass=True, environment=None):
    env = {**os.environ, **(ENVIRONMENT if environment is None else environment),
        'PYTHONDONTWRITEBYTECODE': '1', 'PAGES_DEPLOYMENT_RECORD': raw.decode(),
        'PAGES_APPROVED_DEPLOYMENT_SHA256': gate.digest(raw)}
    argv = [sys.executable, '-B', str(Path(gate.__file__).resolve()), operation,
        '--artifact', str(root), '--source', source, '--core', core, '--repository', repository]
    if operation == 'verify':
        argv += ['--manifest-sha256', manifest]
    result = subprocess.run(argv, env=env, capture_output=True, timeout=20)
    if expected_pass:
        gate.require(result.returncode == 0 and not result.stderr, 'Production CLI did not pass')
        parsed = json.loads(result.stdout)
        gate.require(parsed.get('status') == 'PASS' and parsed.get('operation') == operation,
            'Unexpected production CLI result')
    else:
        gate.require(result.returncode == 1 and not result.stdout, 'Production CLI did not refuse')
        parsed = json.loads(result.stderr)
        gate.require(parsed == {'status': 'REFUSED', 'reason': 'Refused'},
            'Negative control failed for an unexpected reason')
    return {'actualExit': result.returncode, 'result': parsed}

def produce(root, source, core, repository):
    raw = record_bytes(source, core, repository)
    gate.validate_record(raw, gate.digest(raw), source, core, repository,
        ENVIRONMENT, datetime.now(timezone.utc))
    gate.require(root.is_absolute(), 'Synthetic artifact root must be absolute')
    root.mkdir()  # Refuse existing output, including a partial prior producer.
    (root / 'route').mkdir()
    (root / 'index.html').write_text('<!doctype html><html lang="en"><body><h1>Synthetic handoff control</h1></body></html>\n')
    (root / 'route/index.html').write_text('<html lang="en"><body><p>Synthetic second route</p></body></html>\n')
    (root / 'fixture.txt').write_bytes(b'synthetic artifact fixture\n')
    result = production_cli('seal', root, raw, source, core, repository)
    pin = gate.digest(gate.read_small(root / gate.MANIFEST, gate.MANIFEST_LIMIT))
    size = sum(p.stat().st_size for p in root.rglob('*') if p.is_file())
    gate.require(size <= FIXTURE_LIMIT, 'Synthetic artifact exceeds its byte bound')
    return {'record_base64': base64.b64encode(raw).decode(), 'record_sha256': gate.digest(raw),
        'manifest_sha256': pin, 'artifact_bytes': size,
        'production_cli': result, 'source': source, 'core': core, 'repository': repository}

def consume(root, source, core, repository, encoded_record, approved_record, manifest):
    raw, record = selected_record(encoded_record, approved_record)
    positive = production_cli('verify', root, raw, source, core, repository, manifest)
    original = gate.inventory(root)
    gate.require(sum(v['bytes'] for v in original.values())
        + (root / gate.MANIFEST).stat().st_size <= FIXTURE_LIMIT, 'Synthetic artifact exceeds its byte bound')
    controls = []
    filesystem = []
    with tempfile.TemporaryDirectory(prefix='pages-handoff-controls-') as scratch:
        def case(name, change=None, **changes):
            target = Path(scratch) / name
            shutil.copytree(root, target)
            if change:
                alias = change(target)
                if isinstance(alias, Path):
                    target = alias
            values = dict(manifest=manifest, source=source, core=core,
                          repository=repository, environment=ENVIRONMENT)
            values.update(changes)
            result = production_cli('verify', target, raw, expected_pass=False, **values)
            controls.append({'name': name, 'actualExit': result['actualExit'], 'reason': 'Refused'})
        def mutate(path):
            data = (path / 'fixture.txt').read_bytes()
            (path / 'fixture.txt').write_bytes(b'X' + data[1:])
        def rewrite_manifest(path):
            mutate(path)
            value = gate.decode((path / gate.MANIFEST).read_bytes(), gate.MANIFEST_LIMIT)
            value['files'] = gate.inventory(path)
            (path / gate.MANIFEST).write_bytes(gate.encode(value))
        case('same-size-change', mutate)
        case('added-file', lambda p: (p / 'extra.txt').write_bytes(b'extra'))
        case('removed-file', lambda p: (p / 'fixture.txt').unlink())
        case('rewritten-manifest', rewrite_manifest)
        case('wrong-manifest-pin', manifest='0' * 64)
        case('record-bytes', lambda p: (p / gate.RECORD).write_bytes(raw + b'\n'))
        case('wrong-source', source=('0' if source[0] != '0' else '1') + source[1:])
        case('wrong-core', core=('0' if core[0] != '0' else '1') + core[1:])
        case('wrong-repository', repository='Other/synthetic-status')
        case('wrong-endpoint', environment={**ENVIRONMENT, 'VITE_GATEWAY': 'https://other.example.invalid'})
        if sys.platform == 'linux':
            def root_link(path):
                alias = path.parent / 'root-alias'
                alias.symlink_to(path, target_is_directory=True)
                return alias
            operations = [
                ('symlink-file', lambda p: (p / 'alias.txt').symlink_to(p / 'fixture.txt')),
                ('symlink-root', root_link),
                ('hardlink-file', lambda p: os.link(p / 'fixture.txt', p / 'alias.txt')),
                ('fifo-file', lambda p: os.mkfifo(p / 'unexpected')),
            ]
            for name, operation in operations:
                case(name, operation)
                filesystem.append(name)
    try:
        gate.verify(root, raw, approved_record, source, core, repository, ENVIRONMENT,
                    gate.timestamp(record['validUntil']), manifest)
    except gate.Refused:
        controls.append({'name': 'expiry-at-record-end', 'actualExit': None,
                         'mode': 'production API with controlled UTC time', 'reason': 'Refused'})
    else:
        raise gate.Refused('Expiry control did not refuse')
    gate.require(gate.inventory(root) == original
        and gate.digest((root / gate.MANIFEST).read_bytes()) == manifest,
        'Controls changed the downloaded artifact')
    return {'status': 'PASS', 'operation': 'consume', 'source': source, 'core': core,
        'repository': repository, 'manifestSha256': manifest,
        'recordSha256': approved_record, 'positiveProductionCli': positive,
        'negativeControls': controls, 'actualLinuxFilesystemControls': filesystem,
        'inputArtifactPreserved': True, 'syntheticTextOnly': True,
        'realChainEvidenceOrDeploymentQualified': False}

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['produce', 'consume'])
    parser.add_argument('--artifact', required=True, type=Path)
    parser.add_argument('--source', required=True)
    parser.add_argument('--core', required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--github-output', action='store_true')
    args = parser.parse_args(argv)
    try:
        if args.operation == 'produce':
            result = produce(args.artifact, args.source, args.core, args.repository)
            if args.github_output:
                with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
                    for name in ['record_base64', 'record_sha256', 'manifest_sha256']:
                        output.write(name + '=' + result[name] + '\n')
            result = {key: value for key, value in result.items() if key != 'record_base64'}
            result.update(status='PASS', operation='produce', syntheticTextOnly=True)
        else:
            result = consume(args.artifact, args.source, args.core, args.repository,
                os.environ.get('SYNTHETIC_RECORD_BASE64', ''),
                os.environ.get('SYNTHETIC_RECORD_SHA256', ''),
                os.environ.get('SYNTHETIC_MANIFEST_SHA256', ''))
        print(json.dumps(result, sort_keys=True))
        return 0
    except (gate.Refused, OSError, ValueError) as error:
        print(json.dumps({'status': 'REFUSED', 'reason': type(error).__name__}), file=sys.stderr)
        return 1

if __name__ == '__main__':
    sys.exit(main())
