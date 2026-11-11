@echo off
setlocal
if defined VCVARS32 (
  set "VCVARS=%VCVARS32%"
) else (
  set "VCVARS=%ProgramFiles%\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars32.bat"
)
if not exist "%VCVARS%" (
  echo vcvars32.bat not found. Set VCVARS32 to the full path of vcvars32.bat.
  exit /b 1
)
call "%VCVARS%" >nul
if errorlevel 1 exit /b 1
cd /d "%~dp0"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Brepro /Fogrowth.obj growth.cpp
if errorlevel 1 exit /b 1
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x07100000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /Brepro /MERGE:.rdata=.text /MAP:growth.map /OUT:growth.payload growth.obj
