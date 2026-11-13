@echo off
setlocal
call "C:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars32.bat" >nul
if errorlevel 1 exit /b 1
cd /d "%~dp0plugins\DpsMeter"
rc /nologo /fo DpsMeter.res DpsMeter.rc
if errorlevel 1 exit /b 1
cl /nologo /DUNICODE /D_UNICODE /std:c++17 /utf-8 /O2 /MT /EHsc /Brepro /LD DpsMeter.cpp DpsMeter.res /link /Brepro /OUT:DpsMeter.dll user32.lib gdi32.lib gdiplus.lib ole32.lib
if errorlevel 1 exit /b 1
cd /d "%~dp0plugins\CombatPower"
rc /nologo /fo CombatPower.res CombatPower.rc
if errorlevel 1 exit /b 1
cl /nologo /DUNICODE /D_UNICODE /std:c++17 /utf-8 /O2 /MT /EHsc /Brepro /LD CombatPower.cpp CombatPowerFormula.cpp CombatPower.res /link /Brepro /OUT:CombatPower.dll user32.lib gdi32.lib
exit /b %errorlevel%
