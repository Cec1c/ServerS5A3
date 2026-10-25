@echo off
setlocal EnableExtensions DisableDelayedExpansion
cd /d "%~dp0"
python deploy\windows\game_update.py %*
exit /b %errorlevel%
