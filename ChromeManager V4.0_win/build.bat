@echo off
setlocal

cd /d "%~dp0"
title ChromeManager V4.0 Build Tool

set "POWERSHELL_EXE=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"

if not exist "%POWERSHELL_EXE%" (
    echo PowerShell is required to run this build tool.
    echo Please use Windows 10/11 with Windows PowerShell installed.
    pause
    exit /b 1
)

"%POWERSHELL_EXE%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1"
set "EXIT_CODE=%errorlevel%"

if not "%EXIT_CODE%"=="0" (
    echo.
    echo Build bootstrap exited with code %EXIT_CODE%.
)

exit /b %EXIT_CODE%
