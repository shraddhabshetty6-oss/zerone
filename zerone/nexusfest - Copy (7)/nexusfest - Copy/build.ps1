# PowerShell build script for BinStudentDB
param(
    [string]$Target = "all"
)

if (-not (Test-Path "bin")) {
    New-Item -ItemType Directory -Path "bin" | Out-Null
}

switch ($Target) {
    "all" {
        Write-Host "Building production binary: bin/studentdb.exe"
        go build -o bin/studentdb.exe ./cmd/studentdb
        Write-Host "Building faultinject binary: bin/studentdb-fi.exe"
        go build -tags faultinject -o bin/studentdb-fi.exe ./cmd/studentdb
        Write-Host "Building race binary: bin/studentdb-race.exe"
        go build -race -o bin/studentdb-race.exe ./cmd/studentdb
    }
    "prod" {
        go build -o bin/studentdb.exe ./cmd/studentdb
    }
    "fi" {
        go build -tags faultinject -o bin/studentdb-fi.exe ./cmd/studentdb
    }
    "race" {
        go build -race -o bin/studentdb-race.exe ./cmd/studentdb
    }
    "test" {
        go test -v ./...
    }
    default {
        Write-Host "Unknown target: $Target"
    }
}

