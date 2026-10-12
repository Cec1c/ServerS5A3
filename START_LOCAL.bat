@echo off
setlocal
cd /d "%~dp0"
docker.exe info >nul 2>&1
if errorlevel 1 (
  docker.exe desktop start
  if errorlevel 1 goto failed
)
call DOCKER.bat up
if errorlevel 1 goto failed
call LOCAL_CLIENT.bat
exit /b %errorlevel%
:failed
echo Local environment could not start. Review the error above.
pause
exit /b 1
