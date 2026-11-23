@echo off
setlocal
if not defined VSCMD_VER (
  echo Run this file in an x86 Native Tools Command Prompt for Visual Studio.
  exit /b 1
)
cd /d "%~dp0"
cl /nologo /O2 /MT /c zlib\adler32.c zlib\crc32.c zlib\inffast.c zlib\inflate.c zlib\inftrees.c zlib\uncompr.c zlib\zutil.c || exit /b 1
set "zobjs=adler32.obj crc32.obj inffast.obj inflate.obj inftrees.obj uncompr.obj zutil.obj"
cl /nologo /W4 /WX /O2 /MT /EHsc /std:c++17 /LD AtlasWorkers101.cpp %zobjs% /link /DEF:AtlasWorkers101.def /OUT:AtlasWorkers101.dll || exit /b 1
cl /nologo /W4 /WX /O2 /MT /EHsc /std:c++17 check_workers.cpp %zobjs% /link /OUT:check_workers.exe
