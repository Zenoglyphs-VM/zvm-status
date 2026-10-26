"""Bounded synthetic controls; no project service or deployment is used."""
from datetime import datetime, timedelta, timezone
import copy
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import pages_gate as gate

class PagesGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='pages-gate-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'site'
        self.root.mkdir()
        (self.root / 'index.html').write_text('<!doctype html><html><body><h1>Status</h1></body></html>')
        (self.root / 'rpc').mkdir()
        (self.root / 'rpc/index.html').write_text('<html><body class="rpc">Wallet settings</body></html>')
        (self.root / 'app.js').write_text('export const test = true;')
        self.now = datetime.now(timezone.utc).replace(microsecond=0)
        stamp = lambda delta: (self.now + timedelta(seconds=delta)).strftime('%Y-%m-%dT%H:%M:%SZ')
        self.source, self.core, self.repository = '1' * 40, '2' * 40, 'Example/status'
        self.environment = {'VITE_ZVM_NODE': 'https://node.example.invalid/zvm',
            'VITE_GATEWAY': 'https://gateway.example.invalid',
            'VITE_NOM_NODE': 'wss://nom.example.invalid/zenon'}
        self.record = {'schema': 1, 'repository': self.repository, 'sourceCommit': self.source,
            'coreCommit': self.core, 'basePath': '/status/', 'chainId': '7340469',
            'chainEpoch': '3' * 64, 'releaseManifestSha256': '4' * 64,
            'qualificationEvidenceSha256': '5' * 64, 'validFrom': stamp(-60), 'validUntil': stamp(3600),
            'environment': self.environment, 'scope': gate.SCOPE}

    def args(self, record=None, **changes):
        raw = gate.encode(self.record if record is None else record)
        values = dict(raw=raw, approved=gate.digest(raw), source=self.source, core=self.core,
            repository=self.repository, environment=self.environment, now=self.now)
        values.update(changes)
        return values

    def sealed(self):
        manifest = gate.seal(self.root, **self.args())
        return manifest, gate.digest((self.root / gate.MANIFEST).read_bytes())

    def verify(self, pin):
        return gate.verify(self.root, **self.args(), manifest_sha256=pin)

    def test_round_trip_and_visible_record_in_both_routes(self):
        manifest, pin = self.sealed()
        self.assertEqual(self.verify(pin), manifest)
        self.assertEqual(len(manifest['files']), 5)
        self.assertEqual((self.root / gate.RECORD).read_bytes(), self.args()['raw'])
        for route in ['index.html', 'rpc/index.html']:
            text = (self.root / route).read_text()
            self.assertEqual(text.count(gate.MARKER), 1)
            self.assertIn(self.record['chainEpoch'], text)
            self.assertIn('/status/deployment.html', text)
            self.assertIn(self.record['validUntil'], text)
        self.assertIn('does not prove the current chain epoch', (self.root / gate.PAGE).read_text())

    def test_absent_wrong_or_noncanonical_approval_refused(self):
        for pin in ['', '0' * 64, self.args()['approved'].upper()]:
            with self.subTest(pin_length=len(pin)), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(approved=pin))

    def test_exact_approval_bytes_not_semantic_json_equality(self):
        values = self.args()
        values['raw'] = json.dumps(self.record, separators=(',', ':')).encode()
        with self.assertRaises(gate.Refused):
            gate.validate_record(**values)

    def test_unknown_missing_and_wrong_type_fields(self):
        cases = []
        changed = copy.deepcopy(self.record); changed['unknown'] = True; cases.append(changed)
        changed = copy.deepcopy(self.record); del changed['chainEpoch']; cases.append(changed)
        for key, value in [('schema', True), ('chainId', 7340469), ('chainId', '01'),
            ('chainId', str(1 << 64)), ('chainEpoch', '3' * 63), ('chainEpoch', 'F' * 64),
            ('qualificationEvidenceSha256', None), ('scope', 'production-ready'),
            ('sourceCommit', '0' * 40), ('coreCommit', '0' * 40), ('repository', 'Other/status'),
            ('basePath', '/different/'), ('validUntil', '2030-02-31T00:00:00Z')]:
            changed = copy.deepcopy(self.record); changed[key] = value; cases.append(changed)
        for index, record in enumerate(cases):
            with self.subTest(case=index), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(record))

    def test_wrong_runtime_source_core_repository_and_environment(self):
        for changes in [{'source':'9'*40}, {'core':'9'*40}, {'repository':'Other/status'},
            {'environment':{}}, {'environment':{**self.environment,'VITE_GATEWAY':'https://other.example.invalid'}}]:
            with self.subTest(changes=list(changes)), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(**changes))

    def test_validity_boundaries_and_no_revival(self):
        start, end = gate.timestamp(self.record['validFrom']), gate.timestamp(self.record['validUntil'])
        gate.validate_record(**self.args(now=start))
        for now in [start-timedelta(seconds=1), end, end+timedelta(days=1)]:
            with self.subTest(now=now), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(now=now))

    def test_credential_query_private_and_insecure_endpoints(self):
        cases = [('VITE_ZVM_NODE', value) for value in ['http://node.example.invalid',
            'https://user:secret@node.example.invalid', 'https://node.example.invalid/?token=secret',
            'https://node.example.invalid/#secret', 'https://127.0.0.1', 'https://10.0.0.1',
            'https://localhost', 'https://node.local', 'https://node', 'https://node.example.invalid:0',
            'https://node.example.invalid:999999', 'https://node.example.invalid/ space',
            'https://node.example.invalid\\@other.example.invalid', 'https://[invalid']]
        cases.append(('VITE_NOM_NODE', 'https://nom.example.invalid'))
        for name, value in cases:
            changed = copy.deepcopy(self.record); changed['environment'][name] = value
            with self.subTest(case=value), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(changed, environment=changed['environment']))

    def test_duplicate_nonfinite_malformed_and_oversized_json(self):
        for raw in [b'{"schema":1,"schema":1}', b'{"schema":NaN}', b'\xff', b'{', b'[]', b'x'*(gate.RECORD_LIMIT+1)]:
            with self.subTest(bytes=len(raw)), self.assertRaises(gate.Refused):
                gate.validate_record(**self.args(raw=raw, approved=gate.digest(raw)))

    def test_missing_index_and_empty_index(self):
        (self.root/'index.html').unlink()
        with self.assertRaises(gate.Refused): gate.inventory(self.root)
        (self.root/'index.html').write_bytes(b'')
        with self.assertRaises(gate.Refused): gate.inventory(self.root)

    def test_reserved_outputs_refuse_before_html_mutation(self):
        original = (self.root/'index.html').read_bytes()
        for name in [gate.RECORD, gate.PAGE, gate.MANIFEST]:
            with self.subTest(name=name):
                path = self.root/name; path.write_bytes(b'foreign output')
                with self.assertRaises(gate.Refused): gate.seal(self.root, **self.args())
                self.assertEqual((self.root/'index.html').read_bytes(), original)
                self.assertEqual(path.read_bytes(), b'foreign output')
                path.unlink()

    def test_repeated_seal_refused_and_original_retained(self):
        self.sealed()
        before = {p.relative_to(self.root).as_posix():p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        with self.assertRaises(gate.Refused): gate.seal(self.root, **self.args())
        self.assertEqual(before, {p.relative_to(self.root).as_posix():p.read_bytes() for p in self.root.rglob('*') if p.is_file()})

    def test_wrong_body_and_existing_marker_refuse_without_writes(self):
        for text in ['<html>No body</html>', '<body></body><body></body>', '<body>'+gate.MARKER+'</body>']:
            with self.subTest(text=text):
                (self.root/'index.html').write_text(text)
                with self.assertRaises(gate.Refused): gate.seal(self.root, **self.args())
                self.assertFalse((self.root/gate.RECORD).exists())

    def test_asset_mutation_added_and_removed_files_refused(self):
        _, pin = self.sealed()
        original = (self.root/'app.js').read_bytes()
        (self.root/'app.js').write_bytes(b'changed')
        with self.assertRaises(gate.Refused): self.verify(pin)
        (self.root/'app.js').write_bytes(original)
        (self.root/'extra.js').write_bytes(b'extra')
        with self.assertRaises(gate.Refused): self.verify(pin)
        (self.root/'extra.js').unlink(); (self.root/'app.js').unlink()
        with self.assertRaises(gate.Refused): self.verify(pin)

    def test_mutated_manifest_cannot_reauthorize_changed_asset(self):
        manifest, pin = self.sealed()
        (self.root/'app.js').write_text('changed')
        manifest['files'] = gate.inventory(self.root)
        (self.root/gate.MANIFEST).write_bytes(gate.encode(manifest))
        with self.assertRaises(gate.Refused): self.verify(pin)

    def test_manifest_pin_is_required_and_exact(self):
        self.sealed()
        for pin in ['', '0'*64, 'invalid']:
            with self.subTest(pin_length=len(pin)), self.assertRaises(gate.Refused): self.verify(pin)

    def test_record_tampering_refused_even_if_manifest_recomputed(self):
        manifest, _ = self.sealed()
        (self.root/gate.RECORD).write_bytes(b'{}')
        manifest['files'] = gate.inventory(self.root)
        raw = gate.encode(manifest); (self.root/gate.MANIFEST).write_bytes(raw)
        with self.assertRaises(gate.Refused): self.verify(gate.digest(raw))

    def test_manifest_unknown_and_numeric_alias_fields_refused(self):
        manifest, _ = self.sealed()
        for mutation in ['unknown', 'boolean-size']:
            changed = copy.deepcopy(manifest)
            if mutation == 'unknown': changed['unknown'] = True
            else: changed['files']['app.js']['bytes'] = True
            raw = gate.encode(changed); (self.root/gate.MANIFEST).write_bytes(raw)
            with self.subTest(case=mutation), self.assertRaises(gate.Refused): self.verify(gate.digest(raw))

    def test_expiry_rechecked_at_deploy_without_rebuild(self):
        _, pin = self.sealed()
        with self.assertRaises(gate.Refused):
            gate.verify(self.root, **self.args(now=gate.timestamp(self.record['validUntil'])), manifest_sha256=pin)

    def test_case_alias_and_hidden_files_refused(self):
        for name in ['INDEX.HTML', '.env']:
            with self.subTest(name=name):
                path = self.root/name
                # Case aliases are synthetic on case-insensitive hosts; this
                # test uses the walk names without creating an actual alias.
                if name == 'INDEX.HTML':
                    real_walk = os.walk
                    def walk(*args, **kwargs):
                        for parent, dirs, files in real_walk(*args, **kwargs):
                            yield parent, dirs, files+(['INDEX.HTML'] if Path(parent)==self.root else [])
                    with patch.object(gate.os, 'walk', walk), self.assertRaises(gate.Refused): gate.inventory(self.root)
                else:
                    path.write_bytes(b'synthetic')
                    with self.assertRaises(gate.Refused): gate.inventory(self.root)
                    path.unlink()

    def test_link_special_file_and_hardlink_metadata_refused(self):
        # No real links are created in qualification storage. Exercise the
        # filesystem metadata boundary using the exact stat shapes instead.
        original = (self.root/'app.js').lstat()
        for mode, links in [(stat.S_IFLNK|0o777,1),(stat.S_IFIFO|0o600,1),(stat.S_IFREG|0o644,2)]:
            values = list(original); values[0] = mode; values[3] = links
            with self.subTest(mode=mode,links=links), patch.object(Path,'lstat',return_value=os.stat_result(values)), self.assertRaises(gate.Refused):
                gate.ordinary(self.root/'app.js')

    def test_relative_root_and_root_link_refused(self):
        with self.assertRaises(gate.Refused): gate.inventory(Path('relative'))
        original = self.root.lstat(); values=list(original); values[0]=stat.S_IFLNK|0o777
        with patch.object(Path,'lstat',return_value=os.stat_result(values)), self.assertRaises(gate.Refused): gate.root_check(self.root)

    def test_byte_count_entry_and_html_limits(self):
        for field, limit in [('ARTIFACT_LIMIT',8),('FILE_LIMIT',2),('HTML_LIMIT',16)]:
            with self.subTest(field=field), patch.object(gate,field,limit), self.assertRaises(gate.Refused): gate.seal(self.root,**self.args())

    def test_digest_detects_same_size_mutation(self):
        _, pin = self.sealed()
        path=self.root/'app.js'; data=path.read_bytes(); path.write_bytes(b'X'+data[1:])
        with self.assertRaises(gate.Refused): self.verify(pin)

    def test_cli_check_seal_verify_outputs_and_failing_output_redaction(self):
        values=self.args()
        env={**os.environ, **self.environment, 'PAGES_DEPLOYMENT_RECORD':values['raw'].decode(),
            'PAGES_APPROVED_DEPLOYMENT_SHA256':values['approved']}
        command=[sys.executable,'-B',str(Path(gate.__file__).resolve())]
        tail=['--source',self.source,'--core',self.core,'--repository',self.repository]
        checked=subprocess.run(command+['check']+tail,env=env,capture_output=True,timeout=10)
        self.assertEqual(checked.returncode,0,checked.stderr)
        output=Path(self.temp.name)/'github-output'; env['GITHUB_OUTPUT']=str(output)
        sealed=subprocess.run(command+['seal','--artifact',str(self.root),'--github-output']+tail,env=env,capture_output=True,timeout=10)
        self.assertEqual(sealed.returncode,0,sealed.stderr)
        pin=json.loads(sealed.stdout)['manifestSha256']; self.assertEqual(output.read_text(),'manifest_sha256='+pin+'\n')
        verified=subprocess.run(command+['verify','--artifact',str(self.root),'--manifest-sha256',pin]+tail,env=env,capture_output=True,timeout=10)
        self.assertEqual(verified.returncode,0,verified.stderr)
        env['PAGES_APPROVED_DEPLOYMENT_SHA256']='secret-private-context'
        refused=subprocess.run(command+['check']+tail,env=env,capture_output=True,timeout=10)
        self.assertEqual(refused.returncode,1)
        self.assertNotIn(b'secret-private-context',refused.stderr+refused.stdout)
        self.assertNotIn(b'example.invalid',refused.stderr+refused.stdout)

if __name__ == '__main__':
    unittest.main(verbosity=2)
