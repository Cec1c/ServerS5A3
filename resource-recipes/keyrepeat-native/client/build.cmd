@echo off
setlocal
if "%~1"=="" exit /b 2
where cl >nul 2>nul
if errorlevel 1 exit /b 2
if not exist "%~1" mkdir "%~1"
if errorlevel 1 exit /b 1
pushd "%~1"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Brepro /Fokey_repeat.obj "%~dp0key_repeat.cpp"
if errorlevel 1 (popd & exit /b 1)
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x06f90000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /Brepro /MERGE:.rdata=.text /MAP:key_repeat.map /OUT:key_repeat.payload key_repeat.obj
set "key_repeat_build_exit=%errorlevel%"
popd
exit /b %key_repeat_build_exit%
