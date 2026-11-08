"""Explicit, pinned offline fixtures; never uses the mutable installed client."""
from pathlib import Path
import os, hashlib
S=Path(__file__).resolve().parent
def required(name):
 value=os.environ.get(name)
 if not value:raise ValueError(name+' must name an explicit offline fixture/output path')
 return Path(value).resolve()
D=required('KR_VERIFY_OUTPUT');BASE=required('KR_BASE_EXE')
assert D.is_dir() and BASE.is_file()
assert hashlib.sha256(BASE.read_bytes()).hexdigest()=='686eaa069ba3cc374520d249628efc9de89bf0892ad45ad1e4bd2d2206b08dc5'
def old002_build():
 p=required('KR_OLD002_BUILD')
 for n,digest in OLD002_PINS.items():assert hashlib.sha256((p/n).read_bytes()).hexdigest()==digest
 return p

OLD002_PINS={'key_repeat.payload': '4ea74c3cba76a24680e3290763f60a97e9e20a5bc541ab353fb07eb091421024', 'key_repeat.map': '20230ce0197e459f16220621ea709ff1645af8ab55aa38f5a7734e66b4bee1d7'}
