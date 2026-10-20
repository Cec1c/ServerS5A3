@echo off
setlocal
cd /d "%~dp0"
python.exe deploy\windows\remote_client.py
if errorlevel 1 pause
