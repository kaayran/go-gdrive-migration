@echo off
setlocal
cd /d "%~dp0"
call "%~dp0project-vars.bat"

if not exist ".\dist\%APP_EXE%" (
  echo [ERROR] Binary not found: .\dist\%APP_EXE%
  echo Build first: .\build.ps1 -Target win
  echo.
  pause
  exit /b 1
)

echo Running %APP_NAME%...
echo.
".\dist\%APP_EXE%" --config config.yaml %*
set "ERR=%ERRORLEVEL%"

echo.
if not "%ERR%"=="0" (
  echo Finished with error, exit code: %ERR%
) else (
  echo Finished successfully.
)
echo.
pause
exit /b %ERR%
