import copy
import unittest
import client_pairing as W

def pair():
 return dict(kind="paired",exeSha256=W.BLACKDIAMOND_FORMAL_SHA,clientFiles=copy.deepcopy(W.BLACKDIAMOND_FORMAL_FILES))

class ClientPairingTests(unittest.TestCase):
 def test_omitting_entire_dependency_object_fails_at_pair_boundary(self):
   for exe in (W.BLACKDIAMOND_FORMAL_SHA,W.FEATURE_CLIENT_SHA):
    for kind in ('paired','exe-only'):
     with self.subTest(exe=exe,kind=kind):
      old=dict(exeSha256='a'*64,pvfSha256='b'*64,channelSha256='c'*64)
      p=dict(kind=kind,baseExeSha256=old['exeSha256'],basePvfSha256=old['pvfSha256'],exeSha256=exe,pvfSha256=old['pvfSha256'] if kind=='exe-only' else 'd'*64,channelSha256=old['channelSha256'])
      with self.assertRaises(ValueError):W.checked_client_files(p)
 def test_each_mandatory_runtime_dependency_is_required_and_exact(self):
   for name in W.BLACKDIAMOND_FORMAL_FILES:
    for mutation in ('missing','sha','length'):
     with self.subTest(file=name,mutation=mutation):
      p=pair()
      if mutation=='missing':del p['clientFiles'][name]
      elif mutation=='sha':p['clientFiles'][name]['sha256']='0'*64
      else:p['clientFiles'][name]['bytes']+=1
      with self.assertRaises(ValueError):W.checked_client_files(p)
 def test_test_plugins_and_extra_dll_are_not_formal_dependencies(self):
   for name,digest in [('DpsMeter.dll','8690a2db4e9fc951c57603e6e281d2e24041f57a7afcd72289a08d2b875b7d75'),('CombatPower.dll','a1699ffed6a1be9d0298588f80d8d9e925687da81fd609548d50ac743619ec37')]:
    p=pair();p['clientFiles'][name]['sha256']=digest
    with self.subTest(name=name),self.assertRaises(ValueError):W.checked_client_files(p)
   p=pair();p['clientFiles']['other.dll']={'sha256':'a'*64,'bytes':1}
   with self.assertRaises(ValueError):W.checked_client_files(p)
 def test_new_variant_cannot_use_exe_only_or_unknown_plugin_admission(self):
   p=pair();p['kind']='exe-only';p['clientFiles']={'DNF.exe':p['clientFiles']['DNF.exe']}
   with self.assertRaises(ValueError):W.checked_client_files(p)
   p=pair();p['exeSha256']='f'*64;p['clientFiles']['DNF.exe']['sha256']='f'*64
   with self.assertRaises(ValueError):W.checked_client_files(p)
 def test_existing_formal_pair_remains_valid(self):
   p=pair();p['exeSha256']=W.FEATURE_CLIENT_SHA;p['clientFiles']=copy.deepcopy(W.FEATURE_CLIENT_FILES)
   self.assertEqual(W.checked_client_files(p),W.FEATURE_CLIENT_FILES)
