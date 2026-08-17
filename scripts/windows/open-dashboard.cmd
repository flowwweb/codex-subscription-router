@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0open-dashboard.ps1"
exit /b %ERRORLEVEL%
