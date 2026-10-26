"""Gateway migration checks use temporary files and a pinned mock endpoint."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('gm_worker', Path(__file__).with_name('game_update.py'))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class ManagementChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for rel in ['runtime/player-gateway', 'runtime/purplemagic/indexes']:
            (self.root / rel).mkdir(parents=True)
        self.pvf = self.root / 'runtime/Script.pvf'
        self.pvf.write_bytes(b'approved-pvf')
        digest = self.sha(self.pvf)
        self.indexes = self.root / 'runtime/purplemagic/indexes'
        names = ['items.json', 'pvf-directory.bin', 'item-details-v3-' + digest + '.bin',
                 'quests-v1-' + digest + '.bin', 'professions-v1-' + digest + '.bin', 'expert-jobs-v1-' + digest + '.json']
        for name in names:
            (self.indexes / name).write_bytes(name.encode())
        self.certificate = self.root / 'cert.pem'
        self.certificate.write_text('certificate')
        self.cfg = {'certificate': str(self.certificate), 'launcher_hub': {'admin_listen': '127.0.0.1:10999'},
                    'management': {'listen': '127.0.0.1:10999', 'pvfPath': str(self.pvf), 'pvfSha256': digest,
                                   'itemIndexPath': str(self.indexes / 'items.json'), 'pvfIndexPath': str(self.indexes / 'pvf-directory.bin')}}
        self.active = {'gatewaySha256': 'a' * 64, 'pvfSha256': digest,
                       'indexes': {name: self.sha(self.indexes / name) for name in names}}
        self.save()
        self.connection = Mock()
        self.connection.sock.getpeercert.return_value = b'certificate'
        response = self.connection.getresponse.return_value
        response.status = 200
        response.read.return_value = b'{"configured":true,"toolVersion":"90cn-gm-v1"}'

    def sha(self, path):
        if str(path).replace('\\', '/') == '/proc/123/exe':
            return 'a' * 64
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def save(self):
        (self.root / 'runtime/player-gateway/gateway.json').write_text(json.dumps(self.cfg))
        (self.root / 'runtime/purplemagic/active.json').write_text(json.dumps(self.active))

    def check(self):
        with patch('http.client.HTTPSConnection', return_value=self.connection), patch('ssl.PEM_cert_to_DER_cert', return_value=b'certificate'):
            return worker.gateway_management_snapshot(self.root, lambda *args: '123', self.sha)

    def test_legacy_and_explicit_gateway_selection(self):
        self.assertEqual(worker.management_containers({}), ['server', 'gm', 'db'])
        self.assertEqual(worker.management_containers({'gm_enabled': True}), ['server', 'gm', 'db'])
        self.assertEqual(worker.management_containers({'gm_enabled': False}), ['server', 'db'])

    def test_complete_generation_and_runtime_are_verified_without_writes(self):
        before = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        state, files = self.check()
        self.assertEqual(state['mode'], 'gateway')
        self.assertEqual(len(files), 7)
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()})
        self.connection.request.assert_called_once_with('GET', '/api/status')

    def test_missing_management_blocks_disabled_legacy(self):
        self.cfg.pop('management'); self.save()
        with self.assertRaises(AssertionError): self.check()
        self.connection.connect.assert_not_called()

    def test_mismatched_pvf_and_stale_indexes_fail_before_http(self):
        self.pvf.write_bytes(b'next-pvf')
        with self.assertRaises(AssertionError): self.check()
        self.pvf.write_bytes(b'approved-pvf')
        (self.indexes / 'items.json').write_bytes(b'stale')
        with self.assertRaises(AssertionError): self.check()
        self.connection.connect.assert_not_called()

    def test_incomplete_index_generation_and_public_binding_are_rejected(self):
        self.active['indexes'].pop('items.json'); self.save()
        with self.assertRaises(AssertionError): self.check()
        self.cfg['management']['listen'] = '0.0.0.0:10999'; self.save()
        with self.assertRaises(AssertionError): self.check()
        self.connection.connect.assert_not_called()

    def test_certificate_and_health_mismatch_are_rejected(self):
        self.connection.sock.getpeercert.return_value = b'wrong'
        with self.assertRaises(AssertionError): self.check()
        self.connection.sock.getpeercert.return_value = b'certificate'
        self.connection.getresponse.return_value.status = 503
        with self.assertRaises(AssertionError): self.check()
        self.assertEqual(self.connection.close.call_count, 2)

    def external(self):
        pvf = self.active['pvfSha256']
        self.cfg['management']['catalog'] = {'url': 'http://127.0.0.1:29447', 'token': 'c' * 48, 'pvfSha256': pvf}
        self.cfg['management']['serviceToken'] = 'g' * 48
        self.active['externalGM'] = {'pvfSha256': pvf, 'indexes': self.active['indexes'].copy(), 'catalogManifestHash': 'm' * 64}
        self.save()
        response = self.connection.getresponse.return_value
        response.read.return_value = b'{"data":{"apiVersion":"1","itemIndexReady":true,"features":{"accounts.read":true}}}'
        headers = {'X-PurpleMagic-Protocol': 'purplemagic-service-v1', 'X-PVF-SHA256': pvf}
        response.getheader.side_effect = headers.get
        catalog = Mock()
        catalog.status = 200
        catalog.headers = headers.copy()
        catalog.read.return_value = json.dumps({'value': {'ManifestHash': 'm' * 64}}).encode()
        opener = Mock()
        opener.open.return_value.__enter__ = Mock(return_value=catalog)
        opener.open.return_value.__exit__ = Mock(return_value=False)
        return opener, catalog

    def test_external_identity_catalog_and_gateway_are_verified(self):
        opener, catalog = self.external()
        with patch('urllib.request.build_opener', return_value=opener):
            state, files = self.check()
        self.assertEqual(state['mode'], 'external-gm')
        self.assertEqual(len(files), 7)
        self.assertEqual(self.connection.request.call_args.args, ('GET', '/api/v1/capabilities'))
        self.assertEqual(self.connection.request.call_args.kwargs['headers']['Authorization'], 'Bearer ' + 'g' * 48)
        self.assertNotIn('token', json.dumps(state))

    def test_external_catalog_generation_and_missing_receipt_are_rejected(self):
        opener, catalog = self.external()
        catalog.headers['X-PVF-SHA256'] = '0' * 64
        with patch('urllib.request.build_opener', return_value=opener):
            with self.assertRaises(AssertionError): self.check()
        self.connection.connect.assert_not_called()
        self.active.pop('externalGM'); self.save()
        with self.assertRaises(AssertionError): self.check()

    def test_external_gateway_protocol_is_required(self):
        opener, _ = self.external()
        self.connection.getresponse.return_value.getheader.side_effect = lambda _: 'wrong'
        with patch('urllib.request.build_opener', return_value=opener):
            with self.assertRaises(AssertionError): self.check()
        self.connection.close.assert_called_once()


if __name__ == '__main__': unittest.main()
