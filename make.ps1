#!/usr/bin/env pwsh
<#
.SYNOPSIS
Cross-platform build/run helper for linkcode.
Usage: ./make.ps1 [build|run|stop|restart|status|clean]
#>

param(
    [Parameter(Position = 0)]
    [ValidateSet("build", "run", "stop", "restart", "status", "clean")]
    [string]$Target = "build"
)

$ErrorActionPreference = "Stop"

# --- platform detection ---
if (Test-Path Variable:IsWindows) {
    # PowerShell Core / 7+
    $IsWin = $IsWindows
} else {
    # Windows PowerShell 5.1
    $IsWin = $true
}

# --- paths ---
$Binary  = if ($IsWin) { "bin/linkcode.exe" } else { "bin/linkcode" }
$Config  = "configs/linkcode.yaml"
$Log     = if ($IsWin) { Join-Path $env:TEMP "linkcode.log" } else { "/tmp/linkcode.log" }
$PidFile = "bin/.linkcode.pid"

# ============================================================================
# helpers
# ============================================================================

function Ensure-BinDir {
    $dir = Split-Path $Binary -Parent
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
}

function Get-PidFromFile {
    if (Test-Path $PidFile) {
        $raw = (Get-Content $PidFile -Raw).Trim()
        if ($raw -match '^\d+$') { return [int]$raw }
    }
    return $null
}

function Test-ProcessAlive($Id) {
    try { Get-Process -Id $Id -ErrorAction Stop | Out-Null; return $true }
    catch { return $false }
}

function Write-Step($Msg) { Write-Host ">>> $Msg" -ForegroundColor Cyan }

# ============================================================================
# targets
# ============================================================================

function Invoke-Build {
    Write-Step "Building..."
    Ensure-BinDir
    go build -o $Binary ./cmd/linkcode/
    Write-Host "  $Binary" -ForegroundColor Green
}

function Invoke-Run {
    Invoke-Stop
    Invoke-Build

    Write-Step "Starting linkcode..."

    if (Test-Path $PidFile) {
        $stale = Get-Content $PidFile -Raw
        Write-Host "  WARNING: stale pid file found (pid $stale), removing." -ForegroundColor Yellow
        Remove-Item $PidFile -Force
    }

    $stdoutLog = if ($IsWin) { Join-Path $env:TEMP "linkcode-stdout.log" } else { $Log }
    $proc = Start-Process -FilePath $Binary -ArgumentList "-config", $Config `
        -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $Log

    Start-Sleep -Seconds 2

    if ($proc.HasExited) {
        Write-Host "  ERROR: linkcode exited immediately (code $($proc.ExitCode))." -ForegroundColor Red
        return
    }

    $proc.Id | Out-File -FilePath $PidFile -NoNewline
    Write-Host "  Started (pid $($proc.Id)). Log: $Log" -ForegroundColor Green
}

function Invoke-Stop {
    Write-Step "Stopping linkcode..."

    $procId = Get-PidFromFile
    if ($procId -and (Test-ProcessAlive $procId)) {
        Write-Host "  Killing pid $procId..."
        Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue

        $timeout = 20  # 10 seconds
        while ((Test-ProcessAlive $procId) -and ($timeout -gt 0)) {
            Start-Sleep -Milliseconds 500
            $timeout--
        }
        if (Test-ProcessAlive $procId) {
            Write-Host "  Process did not stop gracefully, forcing..." -ForegroundColor Yellow
            Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
        }
        Write-Host "  Stopped." -ForegroundColor Green
    } elseif ($procId) {
        Write-Host "  Stale pid file (pid $procId), cleaning up." -ForegroundColor Yellow
    } else {
        Write-Host "  No pid file found." -ForegroundColor Gray
    }

    # Fallback: kill any leftover linkcode processes
    $leftovers = Get-Process -Name "linkcode" -ErrorAction SilentlyContinue
    if ($leftovers) {
        $leftovers | Stop-Process -Force -ErrorAction SilentlyContinue
        Write-Host "  Cleaned up leftover linkcode process(es)." -ForegroundColor Yellow
    }

    Remove-Item -Force -ErrorAction SilentlyContinue $PidFile
}

function Invoke-Restart {
    # run and restart share the same logic: stop → build → start
    Invoke-Run
}

function Invoke-Status {
    $procId = Get-PidFromFile
    if ($procId -and (Test-ProcessAlive $procId)) {
        Write-Host "Running (pid $procId)." -ForegroundColor Green
        $proc = Get-Process -Id $procId
        Write-Host "  StartTime : $($proc.StartTime)"
        Write-Host "  CPU       : $([math]::Round($proc.TotalProcessorTime.TotalSeconds, 1))s"
    } elseif ($procId) {
        Write-Host "Not running (stale pid file for $procId)." -ForegroundColor Yellow
        Remove-Item -Force -ErrorAction SilentlyContinue $PidFile
    } else {
        Write-Host "Not running." -ForegroundColor Gray
    }
}

function Invoke-Clean {
    Write-Step "Cleaning..."
    Remove-Item -Force -ErrorAction SilentlyContinue $Binary
    Remove-Item -Force -ErrorAction SilentlyContinue $PidFile
    if ($IsWin) { Remove-Item -Force -ErrorAction SilentlyContinue $Log }
    Write-Host "  Done." -ForegroundColor Green
}

# ============================================================================
# dispatch
# ============================================================================

switch ($Target) {
    "build"   { Invoke-Build }
    "run"     { Invoke-Run }
    "stop"    { Invoke-Stop }
    "restart" { Invoke-Restart }
    "status"  { Invoke-Status }
    "clean"   { Invoke-Clean }
}
