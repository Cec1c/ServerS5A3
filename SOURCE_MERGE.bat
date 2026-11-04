@echo off
setlocal EnableExtensions DisableDelayedExpansion
python "%~dp0scripts\ci\source_merge.py" %*
exit /b %errorlevel%
