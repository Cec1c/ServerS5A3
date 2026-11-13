@echo off
setlocal
call "C:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars32.bat" >nul
if errorlevel 1 exit /b 1
cd /d "%~dp0"
if not exist build mkdir build
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Brepro /Fobuild\client.obj client.cpp
if errorlevel 1 exit /b 1
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x07120000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /Brepro /MERGE:.rdata=.text /MAP:build\client.map /OUT:build\client.dll build\client.obj
exit /b %errorlevel%
