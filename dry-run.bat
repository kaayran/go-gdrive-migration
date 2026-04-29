@echo off
setlocal
cd /d "%~dp0"

if not exist ".\dist\gdrive-migrate.exe" (
  echo [ERROR] Binary not found: .\dist\gdrive-migrate.exe
  echo Build first: .\build.ps1 -Target win
  echo.
  pause
  exit /b 1
)

echo Running dry-run mode...
echo.
".\dist\gdrive-migrate.exe" --config config.yaml --dry-run %*
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
