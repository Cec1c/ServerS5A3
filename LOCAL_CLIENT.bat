@echo off
setlocal
cd /d "%~dp0"
python deploy\windows\local_client.py
if errorlevel 1 pause
