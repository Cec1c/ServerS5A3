@echo off
setlocal EnableExtensions DisableDelayedExpansion
python "%~dp0scripts\ci\local_verify.py" %*
exit /b %errorlevel%
