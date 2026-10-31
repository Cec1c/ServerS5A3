@echo off
setlocal
if "%~1"=="" (
  echo Usage: build.cmd external-output-directory
  exit /b 2
)
where cl >nul 2>nul
if errorlevel 1 (
  echo Run from an x86 Visual Studio Developer Command Prompt.
  exit /b 2
)
if not exist "%~1" mkdir "%~1"
if errorlevel 1 exit /b 1
pushd "%~1"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Fofestival.obj "%~dp0festival_component.cpp"
if errorlevel 1 (popd & exit /b 1)
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x07000000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /MAP:festival.map /OUT:festival.payload festival.obj
set "festival_build_exit=%errorlevel%"
popd
exit /b %festival_build_exit%
