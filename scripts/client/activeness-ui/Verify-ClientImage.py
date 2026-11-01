"""Validate a PE with Windows SEC_IMAGE_NO_EXECUTE. Never starts a process."""
from pathlib import Path
import ctypes as C,json,hashlib,sys

def check(path):
 path=Path(path).resolve();k=C.WinDLL('kernel32',use_last_error=True)
 k.CreateFileW.argtypes=[C.c_wchar_p,C.c_uint32,C.c_uint32,C.c_void_p,C.c_uint32,C.c_uint32,C.c_void_p];k.CreateFileW.restype=C.c_void_p
 k.CreateFileMappingW.argtypes=[C.c_void_p,C.c_void_p,C.c_uint32,C.c_uint32,C.c_uint32,C.c_wchar_p];k.CreateFileMappingW.restype=C.c_void_p
 k.CloseHandle.argtypes=[C.c_void_p];k.CloseHandle.restype=C.c_int
 f=k.CreateFileW(str(path),0x80000000,7,None,3,0,None)
 if f==C.c_void_p(-1).value:raise C.WinError(C.get_last_error())
 try:
  C.set_last_error(0);h=k.CreateFileMappingW(f,None,0x11000002,0,0,None);error=C.get_last_error()
  if h:k.CloseHandle(h)
 finally:k.CloseHandle(f)
 return dict(path=str(path),sha256=hashlib.sha256(path.read_bytes()).hexdigest(),passed=bool(h),win32Error=error,errorText=C.FormatError(error),method='CreateFileMappingW(PAGE_READONLY | SEC_IMAGE_NO_EXECUTE)',gameProcessCreated=False,gameCodeExecuted=False,importsResolved=False)

if __name__=='__main__':
 results=[check(x) for x in sys.argv[1:]]
 print(json.dumps(results,ensure_ascii=False,indent=2));sys.exit(0 if results and all(x['passed'] for x in results) else 1)
