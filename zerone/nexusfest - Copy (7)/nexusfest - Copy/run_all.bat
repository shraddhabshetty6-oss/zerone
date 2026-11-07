@echo off
setlocal

echo =====================================================================
echo           BinStudentDB -- End-to-End Execution Pipeline
echo =====================================================================
echo.

cd /d "%~dp0"

set "PYTHONPATH=%CD%\python"
set "BIN_DIR=%CD%\bin"
set "DATA_LIVE=%CD%\data\live"
set "GOLDEN_1K=%CD%\data\golden\1000"

echo [Step 0/12] Checking prerequisites...

set "PY_CMD="
where uv >nul 2>nul
if not errorlevel 1 (
    set "PY_CMD=uv run python"
    echo   Found: uv [using uv run python]
)
if "%PY_CMD%"=="" (
    where python >nul 2>nul
    if not errorlevel 1 (
        set "PY_CMD=python"
        echo   Found: python
    )
)

if "%PY_CMD%"=="" (
    echo [ERROR] Python >= 3.11 or uv was not found in PATH!
    echo Please install Python from https://www.python.org/ or uv from https://astral.sh/uv
    goto :error
)

set "GO_CMD="
where go >nul 2>nul
if %ERRORLEVEL% equ 0 (
    set "GO_CMD=go"
    for /f "tokens=*" %%v in ('go version') do echo   Found: %%v
) else (
    if exist "C:\Program Files\Go\bin\go.exe" set "GO_CMD=C:\Program Files\Go\bin\go.exe"
    if exist "C:\Go\bin\go.exe" set "GO_CMD=C:\Go\bin\go.exe"
    if exist "%USERPROFILE%\go\bin\go.exe" set "GO_CMD=%USERPROFILE%\go\bin\go.exe"
    if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "GO_CMD=%LOCALAPPDATA%\Programs\Go\bin\go.exe"
    if not "%GO_CMD%"=="" (
        for /f "tokens=*" %%v in ('"%GO_CMD%" version') do echo   Found: %%v
    ) else (
        echo   [NOTICE] 'go' compiler not found in standard PATH.
        echo   Download Go >= 1.22 from https://go.dev/dl/
    )
)
echo.

echo [Step 1/12] Running Python framework self-tests...
%PY_CMD% -m studentdb_tests selftest
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 2/12] Running Python unit tests...
%PY_CMD% -m unittest discover -s python/tests
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 3/12] Generating 1,000 synthetic student records...
%PY_CMD% -m studentdb_tests gen --count 1000 --seed 42 --out "%GOLDEN_1K%"
if %ERRORLEVEL% neq 0 goto :error
echo.

if "%GO_CMD%"=="" (
    echo =====================================================================
    echo [NOTICE] Go compiler not detected. Python framework tests passed!
    echo To build and run the Go database engine:
    echo   1. Download and install Go ^>= 1.22 from https://go.dev/dl/
    echo   2. Re-run this run_all.bat script
    echo =====================================================================
    goto :done
)

echo [Step 4/12] Compiling Go Binaries into bin/...
if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

echo   Building bin/studentdb.exe (production)...
"%GO_CMD%" build -o "%BIN_DIR%\studentdb.exe" ./cmd/studentdb
if %ERRORLEVEL% neq 0 goto :error

echo   Building bin/studentdb-fi.exe (fault-injection)...
"%GO_CMD%" build -tags faultinject -o "%BIN_DIR%\studentdb-fi.exe" ./cmd/studentdb
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 5/12] Running Go unit tests and architectural layering checks...
"%GO_CMD%" test -v ./...
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 6/12] Initializing fresh database in data/live...
if exist "%DATA_LIVE%" rmdir /s /q "%DATA_LIVE%"
"%BIN_DIR%\studentdb.exe" init --data-dir "%DATA_LIVE%"
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 7/12] Importing 1,000 records from CSV...
"%BIN_DIR%\studentdb.exe" import --data-dir "%DATA_LIVE%" --input "%GOLDEN_1K%\students.csv"
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 8/12] Verifying database consistency (Invariants I1-I10)...
"%BIN_DIR%\studentdb.exe" verify --data-dir "%DATA_LIVE%"
if %ERRORLEVEL% neq 0 goto :error
echo.

echo [Step 9/12] Database Statistics:
"%BIN_DIR%\studentdb.exe" stats --data-dir "%DATA_LIVE%"
echo.

echo [Step 10/12] Running functional test suite...
%PY_CMD% -m studentdb_tests functional --bin "%BIN_DIR%\studentdb.exe" --dataset "%GOLDEN_1K%"
echo.

echo [Step 11/12] Running Torture Testing Scenarios (Crash and Recovery)...
%PY_CMD% -m studentdb_tests torture --bin "%BIN_DIR%\studentdb-fi.exe" --scenario all --iterations 2
echo.

echo [Step 12/12] Final Report Status:
echo Reports have been written to the reports/ directory.
echo.

:done
echo =====================================================================
echo                      PIPELINE COMPLETED SUCCESSFULLY!
echo =====================================================================
exit /b 0

:error
echo.
echo =====================================================================
echo [FAILURE] The pipeline encountered an error.
echo =====================================================================
exit /b 1
