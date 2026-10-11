@echo off
setlocal
cd /d "%~dp0"
python deploy\docker\manage.py %*
exit /b %errorlevel%
