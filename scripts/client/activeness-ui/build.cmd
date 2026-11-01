@echo off
setlocal
if not defined VCINSTALLDIR (
  if not defined VCVARS32 (
    echo Run from an x86 MSVC developer command prompt or set VCVARS32.
    exit /b 2
  )
  call "%VCVARS32%" >nul
  if errorlevel 1 exit /b 2
)
cd /d "%~dp0"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Foactivity.obj activity.cpp
if errorlevel 1 exit /b 1
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x06ef0000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /MAP:activity.map /OUT:activity.payload activity.obj
exit /b %errorlevel%
