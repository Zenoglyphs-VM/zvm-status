"""Offline, fail-closed binding of a Pages build to owner-selected evidence.

This checks bytes and configured approval pins, not the truth of chain evidence.
The build and deploy jobs must use the same source, record and artifact bytes.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import html
import ipaddress
import json
import os
from pathlib import Path
import re
import stat
import sys
from urllib.parse import urlsplit

RECORD_LIMIT = 64 << 10
MANIFEST_LIMIT = 2 << 20
ARTIFACT_LIMIT = 128 << 20
FILE_LIMIT = 4096
HTML_LIMIT = 1 << 20
MANIFEST = 'pages-artifact.json'
RECORD = 'deployment.json'
PAGE = 'deployment.html'
MARKER = '<!-- zvm-pages-deployment -->'
ENV_NAMES = {'VITE_ZVM_NODE', 'VITE_GATEWAY', 'VITE_NOM_NODE'}
SCOPE = 'static-distribution-only; chain and wallet readiness require independent verification'

class Refused(ValueError):
    """The selected inputs cannot qualify this artifact."""

def require(condition, message):
    if not condition:
        raise Refused(message)

def digest(raw):
    return hashlib.sha256(raw).hexdigest()

def encode(value):
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=True) + '\n').encode()

def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result, 'Duplicate JSON member')
        result[key] = value
    return result

def decode(raw, limit):
    require(0 < len(raw) <= limit, 'JSON byte limit exceeded or empty input')
    try:
        return json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
            parse_constant=lambda _: (_ for _ in ()).throw(Refused('Non-finite JSON number')))
    except (UnicodeError, json.JSONDecodeError, RecursionError) as error:
        raise Refused('Malformed JSON') from error

def exact_fields(value, fields):
    require(type(value) is dict and set(value) == set(fields), 'Missing or unknown fields')

def hex_value(value, size):
    return type(value) is str and re.fullmatch('[0-9a-f]{' + str(size) + '}', value) is not None

def timestamp(value):
    require(type(value) is str and re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', value), 'Invalid UTC time')
    try:
        return datetime.strptime(value, '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=timezone.utc)
    except ValueError as error:
        raise Refused('Invalid UTC time') from error

def endpoint(value, name):
    require(type(value) is str and 0 < len(value) <= 2048 and value.isascii(), 'Invalid endpoint')
    require(not any(c.isspace() or ord(c) < 32 or c in '\\<>"\'' for c in value), 'Invalid endpoint characters')
    try:
        parts = urlsplit(value)
        host = parts.hostname
        port = parts.port
    except ValueError as error:
        raise Refused('Malformed endpoint') from error
    expected = 'wss' if name == 'VITE_NOM_NODE' else 'https'
    require(parts.scheme == expected and bool(host) and not parts.username and not parts.password
        and not parts.query and not parts.fragment and port != 0, 'Endpoint requires a public encrypted URL without credentials or query data')
    require(host.isascii() and not host.endswith('.') and '.' in host and host != 'localhost'
        and not host.endswith(('.localhost', '.local')), 'Local endpoint is not eligible for Pages')
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        require(re.fullmatch(r'[a-zA-Z0-9.-]+', host) is not None, 'Invalid DNS hostname')
    else:
        require(address.is_global, 'Private endpoint is not eligible for Pages')

def validate_record(raw, approved, source, core, repository, environment, now):
    require(hex_value(approved, 64) and digest(raw) == approved, 'Owner-selected deployment digest is absent or mismatched')
    record = decode(raw, RECORD_LIMIT)
    exact_fields(record, ['schema', 'repository', 'sourceCommit', 'coreCommit', 'basePath',
        'chainId', 'chainEpoch', 'releaseManifestSha256', 'qualificationEvidenceSha256',
        'validFrom', 'validUntil', 'environment', 'scope'])
    require(type(record['schema']) is int and record['schema'] == 1, 'Unsupported record schema')
    require(hex_value(source, 40) and hex_value(core, 40) and record['sourceCommit'] == source
        and record['coreCommit'] == core, 'Source or Core revision differs')
    require(type(repository) is str and re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository)
        and record['repository'] == repository, 'Repository differs')
    require(record['basePath'] == '/' + repository.split('/')[1] + '/', 'Pages base path differs')
    require(type(record['chainId']) is str and re.fullmatch(r'[1-9][0-9]{0,19}', record['chainId'])
        and int(record['chainId']) < 1 << 64, 'Invalid chain ID')
    require(all(hex_value(record[k], 64) for k in ['chainEpoch', 'releaseManifestSha256', 'qualificationEvidenceSha256']), 'Invalid epoch or evidence digest')
    require(record['scope'] == SCOPE, 'Unsupported qualification claim')
    start, end = timestamp(record['validFrom']), timestamp(record['validUntil'])
    require(start <= now < end, 'Deployment record is not currently valid')
    exact_fields(record['environment'], ENV_NAMES)
    require(type(environment) is dict and set(environment) == ENV_NAMES and environment == record['environment'], 'Build environment differs')
    for name, value in environment.items():
        endpoint(value, name)
    return record

def ordinary(path, directory=False):
    info = path.lstat()
    require((stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)), 'Links or special files are refused')
    if not directory:
        require(info.st_nlink == 1, 'Hard-linked artifact file is refused')
    return info

def signature(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_nlink, info.st_size, info.st_mtime_ns)

def root_check(root):
    require(root.is_absolute(), 'Artifact root must be absolute')
    for parent in reversed([root, *root.parents]):
        ordinary(parent, directory=True)

def read_small(path, limit):
    before = ordinary(path)
    require(before.st_size <= limit, 'File byte limit exceeded')
    with path.open('rb') as handle:
        require(signature(os.fstat(handle.fileno())) == signature(before), 'File changed before reading')
        raw = handle.read(limit + 1)
        after = os.fstat(handle.fileno())
    require(len(raw) <= limit and signature(before) == signature(after) == signature(path.lstat()), 'File changed while reading')
    return raw

def file_digest(path, remaining):
    before = ordinary(path)
    require(before.st_size <= remaining, 'Artifact byte limit exceeded')
    result, length = hashlib.sha256(), 0
    with path.open('rb') as handle:
        require(signature(os.fstat(handle.fileno())) == signature(before), 'File changed before hashing')
        while True:
            chunk = handle.read(min(65536, remaining - length + 1))
            if not chunk:
                break
            length += len(chunk)
            require(length <= remaining, 'Artifact byte limit exceeded')
            result.update(chunk)
        after = os.fstat(handle.fileno())
    require(signature(before) == signature(after) == signature(path.lstat()) and length == before.st_size, 'File changed while hashing')
    return {'bytes': length, 'sha256': result.hexdigest()}

def inventory(root):
    root_check(root)
    result, folded, total, entries = {}, set(), 0, 0
    for parent, dirs, files in os.walk(root, followlinks=False):
        entries += len(dirs) + len(files)
        require(entries <= FILE_LIMIT, 'Artifact entry limit exceeded')
        for name in dirs + files:
            path = Path(parent) / name
            relative = path.relative_to(root).as_posix()
            require(len(relative) <= 240 and len(relative.split('/')) <= 12
                and re.fullmatch(r'[A-Za-z0-9_./-]+', relative) and not any(p.startswith('.') for p in relative.split('/')), 'Unsafe artifact path')
            key = relative.casefold()
            require(key not in folded, 'Case-alias artifact paths are refused')
            folded.add(key)
            ordinary(path, directory=name in dirs)
        for name in sorted(files):
            path = Path(parent) / name
            relative = path.relative_to(root).as_posix()
            if relative == MANIFEST:
                continue
            info = file_digest(path, ARTIFACT_LIMIT - total)
            total += info['bytes']
            result[relative] = info
    require('index.html' in result and result['index.html']['bytes'] > 0, 'Missing nonempty index.html')
    return dict(sorted(result.items()))

def create(path, raw):
    # Never replace any pre-existing output, including a partial previous seal.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    with os.fdopen(fd, 'wb') as handle:
        handle.write(raw)

def seal(root, raw, approved, source, core, repository, environment, now):
    record = validate_record(raw, approved, source, core, repository, environment, now)
    files = inventory(root)
    require(not any((root / name).exists() or (root / name).is_symlink() for name in [MANIFEST, RECORD, PAGE]), 'Artifact was already sealed or has reserved outputs')
    banner = (MARKER + '<aside role="note" style="padding:1em;border:2px solid currentColor">'
        'Static build; chain readiness requires independent verification. '
        '<a href="' + html.escape(record['basePath'], quote=True) + PAGE + '">Deployment record</a> '
        '&middot; Chain ' + record['chainId'] + ' &middot; Epoch ' + record['chainEpoch']
        + ' &middot; Valid until <time>' + record['validUntil'] + '</time></aside>')
    rewrites = {}
    for name in files:
        if name.endswith('.html'):
            text = read_small(root / name, HTML_LIMIT).decode('utf-8')
            require(MARKER not in text and len(re.findall(r'<body(?:\s[^>]*)?>', text, re.I)) == 1, 'HTML requires exactly one body and no prior deployment marker')
            changed = re.sub(r'(<body(?:\s[^>]*)?>)', lambda m: m.group(1) + banner, text, count=1, flags=re.I).encode()
            require(len(changed) <= HTML_LIMIT, 'Sealed HTML byte limit exceeded')
            rewrites[name] = changed
    # All validation above precedes writes. An interrupted seal is unqualified;
    # discard its build scratch and rebuild instead of retrying over it.
    for name, changed in rewrites.items():
        require(file_digest(root / name, ARTIFACT_LIMIT) == files[name], 'HTML changed before sealing')
        (root / name).write_bytes(changed)
    create(root / RECORD, raw)
    details = ''.join('<dt>' + html.escape(k) + '</dt><dd>' + html.escape(str(record[k])) + '</dd>'
        for k in ['repository','sourceCommit','coreCommit','chainId','chainEpoch','releaseManifestSha256',
            'qualificationEvidenceSha256','validFrom','validUntil','scope'])
    create(root / PAGE, ('<!doctype html><html lang="en"><meta charset="utf-8"><title>Deployment record</title>'
        '<body><h1>Static deployment record</h1><p>This record describes the intended build. '
        'It does not prove the current chain epoch, endpoint health, wallet safety or production readiness.</p>'
        '<dl>' + details + '</dl><p><a href="deployment.json">Exact approved record</a></p></body></html>\n').encode())
    manifest = {'schema': 1, 'deploymentRecordSha256': approved, 'scope': SCOPE, 'files': inventory(root)}
    encoded = encode(manifest)
    require(len(encoded) <= MANIFEST_LIMIT, 'Artifact manifest byte limit exceeded')
    create(root / MANIFEST, encoded)
    verify(root, raw, approved, source, core, repository, environment, now, digest(encoded))
    return manifest

def verify(root, raw, approved, source, core, repository, environment, now, manifest_sha256):
    validate_record(raw, approved, source, core, repository, environment, now)
    root_check(root)
    manifest_raw = read_small(root / MANIFEST, MANIFEST_LIMIT)
    require(hex_value(manifest_sha256, 64) and digest(manifest_raw) == manifest_sha256, 'Trusted build manifest digest differs')
    manifest = decode(manifest_raw, MANIFEST_LIMIT)
    exact_fields(manifest, ['schema', 'deploymentRecordSha256', 'scope', 'files'])
    require(type(manifest['schema']) is int and manifest['schema'] == 1
        and manifest['deploymentRecordSha256'] == approved and manifest['scope'] == SCOPE, 'Artifact binding differs')
    require(read_small(root / RECORD, RECORD_LIMIT) == raw, 'Exact approved record differs')
    actual = inventory(root)
    require(type(manifest['files']) is dict and encode(manifest['files']) == encode(actual), 'Artifact bytes or file set differ')
    require(PAGE in actual, 'Missing visible deployment record')
    return manifest

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['check', 'seal', 'verify'])
    parser.add_argument('--artifact', type=Path)
    parser.add_argument('--source', required=True)
    parser.add_argument('--core', required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--manifest-sha256', default='')
    parser.add_argument('--github-output', action='store_true')
    args = parser.parse_args(argv)
    try:
        raw = os.environ.get('PAGES_DEPLOYMENT_RECORD', '').encode('utf-8')
        approved = os.environ.get('PAGES_APPROVED_DEPLOYMENT_SHA256', '')
        env = {name: os.environ.get(name, '') for name in ENV_NAMES}
        values = (raw, approved, args.source, args.core, args.repository, env, datetime.now(timezone.utc))
        if args.operation == 'check':
            validate_record(*values)
            result = {'recordSha256': approved, 'scope': SCOPE}
        else:
            require(args.artifact is not None, 'An artifact path is required')
            if args.operation == 'seal':
                manifest = seal(args.artifact, *values)
                pin = digest(read_small(args.artifact / MANIFEST, MANIFEST_LIMIT))
                if args.github_output:
                    with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
                        output.write('manifest_sha256=' + pin + '\n')
            else:
                manifest = verify(args.artifact, *values, args.manifest_sha256)
                pin = args.manifest_sha256
            result = {'files': len(manifest['files']), 'manifestSha256': pin, 'scope': SCOPE}
        print(json.dumps({'status': 'PASS', 'operation': args.operation, **result}, sort_keys=True))
        return 0
    except (Refused, OSError, UnicodeError) as error:
        # Do not echo configuration, endpoints, file names or raw diagnostics.
        print(json.dumps({'status': 'REFUSED', 'reason': type(error).__name__}), file=sys.stderr)
        return 1

if __name__ == '__main__':
    sys.exit(main())
