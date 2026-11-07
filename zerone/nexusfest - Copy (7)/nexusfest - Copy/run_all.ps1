# BinStudentDB PowerShell Pipeline Execution Script
$ErrorActionPreference = "Stop"

Write-Host "=====================================================================" -ForegroundColor Cyan
Write-Host "          BinStudentDB -- End-to-End Execution Pipeline (PowerShell)   " -ForegroundColor Cyan
Write-Host "=====================================================================" -ForegroundColor Cyan
Write-Host ""

$ROOT = (Get-Location).Path
$env:PYTHONPATH = (Join-Path $ROOT "python")
$BIN_DIR = Join-Path $ROOT "bin"
$DATA_LIVE = Join-Path $ROOT "data\live"
$GOLDEN_1K = Join-Path $ROOT "data\golden\1000"

# Step 0: Check Environment
Write-Host "[Step 0/12] Checking prerequisites..." -ForegroundColor Yellow

$PyCmd = $null
if (Get-Command uv -ErrorAction SilentlyContinue) {
    $PyCmd = "uv run python"
    Write-Host "  Found: uv (using 'uv run python')" -ForegroundColor Green
} elseif (Get-Command python -ErrorAction SilentlyContinue) {
    $PyCmd = "python"
    Write-Host "  Found: python" -ForegroundColor Green
} elseif (Get-Command py -ErrorAction SilentlyContinue) {
    $PyCmd = "py -3"
    Write-Host "  Found: py launcher" -ForegroundColor Green
} else {
    Write-Error "Python >= 3.11 or uv not found in PATH! Please install Python or uv."
}

$GoCmd = $null
if (Get-Command go -ErrorAction SilentlyContinue) {
    $GoCmd = "go"
} else {
    $standardGoPaths = @(
        "C:\Program Files\Go\bin\go.exe",
        "C:\Go\bin\go.exe",
        "$env:USERPROFILE\go\bin\go.exe",
        "$env:LOCALAPPDATA\Programs\Go\bin\go.exe"
    )
    foreach ($p in $standardGoPaths) {
        if (Test-Path $p) {
            $GoCmd = $p
            break
        }
    }
}

if ($GoCmd) {
    $goVer = & $GoCmd version
    Write-Host "  Found: $goVer" -ForegroundColor Green
} else {
    Write-Host "  [NOTICE] Go compiler not detected. (Download from https://go.dev/dl/)" -ForegroundColor Yellow
}
Write-Host ""

# Step 1: Self-tests
Write-Host "[Step 1/12] Running Python framework self-tests..." -ForegroundColor Yellow
& uv run python -m studentdb_tests selftest
Write-Host ""

# Step 2: Unit tests
Write-Host "[Step 2/12] Running Python unit tests..." -ForegroundColor Yellow
& uv run python -m unittest discover -s python/tests
Write-Host ""

# Step 3: Dataset Generation
Write-Host "[Step 3/12] Generating 1,000 synthetic student records..." -ForegroundColor Yellow
& uv run python -m studentdb_tests gen --count 1000 --seed 42 --out "$GOLDEN_1K"
Write-Host ""

if (-not $GoCmd) {
    Write-Host "=====================================================================" -ForegroundColor Yellow
    Write-Host "[NOTICE] Go compiler not found. All Python framework tests succeeded!" -ForegroundColor Yellow
    Write-Host "To compile and execute the Go database engine, install Go (version 1.22 or newer)" -ForegroundColor Yellow
    Write-Host "from https://go.dev/dl/ and re-run this script." -ForegroundColor Yellow
    Write-Host "=====================================================================" -ForegroundColor Yellow
    exit 0
}

# Step 4: Compile Go Binaries
Write-Host "[Step 4/12] Compiling Go Binaries into bin/..." -ForegroundColor Yellow
if (-not (Test-Path $BIN_DIR)) { New-Item -ItemType Directory -Path $BIN_DIR | Out-Null }

Write-Host "  Building bin/studentdb.exe (production)..."
& $GoCmd build -o (Join-Path $BIN_DIR "studentdb.exe") ./cmd/studentdb

Write-Host "  Building bin/studentdb-fi.exe (fault-injection)..."
& $GoCmd build -tags faultinject -o (Join-Path $BIN_DIR "studentdb-fi.exe") ./cmd/studentdb
Write-Host ""

# Step 5: Go unit and layer tests
Write-Host "[Step 5/12] Running Go unit tests and layering checks..." -ForegroundColor Yellow
& $GoCmd test -v ./...
Write-Host ""

# Step 6: Init database
Write-Host "[Step 6/12] Initializing fresh database in data/live..." -ForegroundColor Yellow
if (Test-Path $DATA_LIVE) { Remove-Item -Recurse -Force $DATA_LIVE }
& (Join-Path $BIN_DIR "studentdb.exe") init --data-dir $DATA_LIVE
Write-Host ""

# Step 7: Import records
Write-Host "[Step 7/12] Importing 1,000 records from CSV..." -ForegroundColor Yellow
& (Join-Path $BIN_DIR "studentdb.exe") import --data-dir $DATA_LIVE --input (Join-Path $GOLDEN_1K "students.csv")
Write-Host ""

# Step 8: Verify consistency
Write-Host "[Step 8/12] Verifying database consistency (Invariants I1-I10)..." -ForegroundColor Yellow
& (Join-Path $BIN_DIR "studentdb.exe") verify --data-dir $DATA_LIVE
Write-Host ""

# Step 9: Print stats
Write-Host "[Step 9/12] Database Statistics:" -ForegroundColor Yellow
& (Join-Path $BIN_DIR "studentdb.exe") stats --data-dir $DATA_LIVE
Write-Host ""

# Step 10: Functional test suite
Write-Host "[Step 10/12] Running functional test suite..." -ForegroundColor Yellow
& uv run python -m studentdb_tests functional --bin "$BIN_DIR\studentdb.exe" --dataset "$GOLDEN_1K"
Write-Host ""

# Step 11: Torture testing
Write-Host "[Step 11/12] Running Torture Testing Scenarios (Crash and Recovery)..." -ForegroundColor Yellow
& uv run python -m studentdb_tests torture --bin "$BIN_DIR\studentdb-fi.exe" --scenario all --iterations 2
Write-Host ""

Write-Host "=====================================================================" -ForegroundColor Green
Write-Host "                 PIPELINE COMPLETED SUCCESSFULLY!                    " -ForegroundColor Green
Write-Host "=====================================================================" -ForegroundColor Green
