"""Bounded synthetic handoff controls; no service or application build."""
import base64
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import artifact_handoff as handoff
import pages_gate as gate

class ArtifactHandoffTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='artifact-handoff-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'site'
        self.source, self.core, self.repository = '1' * 40, '2' * 40, 'Example/status'
        self.output = handoff.produce(self.root, self.source, self.core, self.repository)

    def consume(self, root=None, **changes):
        values = dict(root=self.root if root is None else root, source=self.source,
            core=self.core, repository=self.repository,
            encoded_record=self.output['record_base64'],
            approved_record=self.output['record_sha256'], manifest=self.output['manifest_sha256'])
        values.update(changes)
        return handoff.consume(**values)

    def snapshot(self):
        return {p.relative_to(self.root).as_posix(): p.read_bytes()
                for p in self.root.rglob('*') if p.is_file()}

    def test_round_trip_production_CLI_and_negative_controls_preserve_input(self):
        original = self.snapshot()
        result = self.consume()
        self.assertEqual(result['positiveProductionCli']['actualExit'], 0)
        cli = [x for x in result['negativeControls'] if x['actualExit'] is not None]
        expected_filesystem = 4 if sys.platform == 'linux' else 0
        self.assertEqual(len(cli), 10 + expected_filesystem)
        self.assertTrue(all(x['actualExit'] == 1 and x['reason'] == 'Refused' for x in cli))
        expiry = next(x for x in result['negativeControls'] if x['name'] == 'expiry-at-record-end')
        self.assertIsNone(expiry['actualExit'])
        self.assertEqual(len(result['actualLinuxFilesystemControls']), expected_filesystem)
        self.assertEqual(original, self.snapshot())
        self.assertFalse(result['realChainEvidenceOrDeploymentQualified'])

    def test_fixture_is_bounded_text_and_source_bound(self):
        self.assertLessEqual(self.output['artifact_bytes'], handoff.FIXTURE_LIMIT)
        self.assertEqual(len(self.snapshot()), 6)
        record = json.loads((self.root / gate.RECORD).read_bytes())
        self.assertEqual(record['sourceCommit'], self.source)
        self.assertEqual(record['coreCommit'], self.core)
        self.assertEqual(record['environment'], handoff.ENVIRONMENT)
        self.assertEqual(record['chainId'], '1')
        self.assertTrue(all(value.isascii() for value in self.snapshot().values()))

    def test_producer_does_not_overwrite_existing_or_partially_sealed_output(self):
        original = self.snapshot()
        with self.assertRaises(FileExistsError):
            handoff.produce(self.root, self.source, self.core, self.repository)
        self.assertEqual(original, self.snapshot())

    def test_expected_record_requires_exact_external_digest(self):
        for value in ['', '0' * 64, self.output['record_sha256'].upper()]:
            with self.subTest(value=value), self.assertRaises(gate.Refused):
                self.consume(approved_record=value)
        raw = base64.b64decode(self.output['record_base64']) + b'\n'
        with self.assertRaises(gate.Refused):
            self.consume(encoded_record=base64.b64encode(raw).decode())

    def test_malformed_or_oversized_external_record_is_refused(self):
        for value in ['', '%%%', 'x' * (gate.RECORD_LIMIT * 2)]:
            with self.subTest(bytes=len(value)), self.assertRaises(gate.Refused):
                self.consume(encoded_record=value)

    def test_harness_refuses_nonfixture_configuration_even_with_matching_digest(self):
        record = json.loads(base64.b64decode(self.output['record_base64']))
        for change in [{'chainId': '7340469'}, {'chainEpoch': '0' * 64},
                       {'environment': {**handoff.ENVIRONMENT, 'VITE_GATEWAY': 'https://real.example.invalid'}}]:
            changed = copy.deepcopy(record)
            changed.update(change)
            raw = gate.encode(changed)
            with self.subTest(keys=list(change)), self.assertRaises(gate.Refused):
                self.consume(encoded_record=base64.b64encode(raw).decode(), approved_record=gate.digest(raw))

    def test_missing_external_manifest_digest_is_refused(self):
        with self.assertRaises(gate.Refused):
            self.consume(manifest='')

    def test_relocated_directory_uses_original_external_record_and_manifest(self):
        target = Path(self.temp.name) / 'downloaded'
        shutil.copytree(self.root, target)
        result = self.consume(root=target)
        self.assertTrue(result['inputArtifactPreserved'])
        self.assertEqual(result['manifestSha256'], self.output['manifest_sha256'])

    def test_producer_ignores_unrelated_real_configuration(self):
        with patch.dict(os.environ, {'PAGES_DEPLOYMENT_RECORD': 'private-test-context',
            'PAGES_APPROVED_DEPLOYMENT_SHA256': 'private-test-context',
            'VITE_GATEWAY': 'https://unrelated.example.invalid'}):
            other = Path(self.temp.name) / 'other'
            handoff.produce(other, self.source, self.core, self.repository)
        self.assertNotIn(b'private-test-context', (other / gate.RECORD).read_bytes())

    def test_cli_job_outputs_and_redacted_refusal(self):
        command = [sys.executable, '-B', str(Path(handoff.__file__).resolve())]
        common = ['--source', self.source, '--core', self.core, '--repository', self.repository]
        root = Path(self.temp.name) / 'cli-site'
        output = Path(self.temp.name) / 'github-output'
        env = {**os.environ, 'GITHUB_OUTPUT': str(output)}
        result = subprocess.run(command + ['produce', '--artifact', str(root), '--github-output'] + common,
            env=env, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split('=', 1) for line in output.read_text().splitlines())
        self.assertEqual(set(values), {'record_base64', 'record_sha256', 'manifest_sha256'})
        self.assertEqual(gate.digest(base64.b64decode(values['record_base64'])), values['record_sha256'])
        self.assertNotIn(b'record_base64', result.stdout)
        env.update(SYNTHETIC_RECORD_BASE64='private-test-context',
                   SYNTHETIC_RECORD_SHA256='private-test-context', SYNTHETIC_MANIFEST_SHA256='private-test-context')
        refused = subprocess.run(command + ['consume', '--artifact', str(root)] + common,
            env=env, capture_output=True, timeout=20)
        self.assertEqual(refused.returncode, 1)
        self.assertEqual(json.loads(refused.stderr), {'status': 'REFUSED', 'reason': 'Refused'})
        self.assertNotIn(b'private-test-context', refused.stdout + refused.stderr)

if __name__ == '__main__':
    unittest.main(verbosity=2)
