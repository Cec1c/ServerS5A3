@echo off
python "%~dp0verify_bundle.py" %*
exit /b %errorlevel%
