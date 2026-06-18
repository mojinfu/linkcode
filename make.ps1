#!/usr/bin/env pwsh
<#
.SYNOPSIS
Cross-platform build/run helper for linkcode.
Usage: ./make.ps1 [build|run|restart|stop|status|clean] [-daemon]

Targets:
  build    编译
  run      编译 + 重启（kill 旧进程 → 编译 → 启动）
  restart  仅重启  （kill 旧进程 → 启动，不编译）
  stop     停止
  status   查看状态
  clean    清理编译产物

Options:
  -daemon  后台运行（默认前台，可直接 Ctrl+C 停止）
#>

param(
    [Parameter(Position = 0)]
    [ValidateSet("build", "run", "restart", "stop", "status", "clean")]
    [string]$Target = "build",

    [switch]$Daemon
)

$ErrorActionPreference = "Stop"

# --- helpers ---
function Write-Step($Msg) { Write-Host ">>> $Msg" -ForegroundColor Cyan }
function Write-OK($Msg)   { Write-Host "  [OK] $Msg" -ForegroundColor Green }
function Write-Warn($Msg) { Write-Host "  [WARN] $Msg" -ForegroundColor Yellow }
function Write-Err($Msg)  { Write-Host "  [ERROR] $Msg" -ForegroundColor Red }

# --- paths ---
$Binary  = "bin/linkcode.exe"
$Config  = "configs/linkcode.yaml"
$LogDir  = if ($env:TEMP) { $env:TEMP } else { "/tmp" }
$StdoutLog = Join-Path $LogDir "linkcode-stdout.log"
$StderrLog = Join-Path $LogDir "linkcode.log"
$PidFile = "bin/.linkcode.pid"

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

# ============================================================================
# targets
# ============================================================================

function Invoke-Build {
    Write-Step "Building..."
    Ensure-BinDir
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    go build -o $Binary ./cmd/linkcode/
    if ($LASTEXITCODE -ne 0) {
        Write-Err "build failed"
        exit $LASTEXITCODE
    }
    Write-OK "$Binary ($($sw.Elapsed.TotalSeconds.ToString('0.0'))s)"
}

function Invoke-Stop {
    Write-Step "Stopping..."
    $killed = $false

    # 1. Kill by PID file.
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
            Write-Warn "Process did not stop gracefully, forcing..."
            Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
        }
        $killed = $true
    } elseif ($procId) {
        Write-Warn "Stale pid file (pid $procId), cleaning up."
    } else {
        Write-Host "  No pid file found."
    }

    # 2. Fallback: kill any leftover linkcode processes by name.
    $leftovers = Get-Process -Name "linkcode" -ErrorAction SilentlyContinue
    if ($leftovers) {
        $leftovers | Stop-Process -Force -ErrorAction SilentlyContinue
        $killed = $true
        Write-OK "Cleaned up $($leftovers.Count) leftover process(es)."
    }

    Remove-Item -Force -ErrorAction SilentlyContinue $PidFile

    if ($killed) { Write-OK "Stopped." }
    else         { Write-Host "  Already stopped." -ForegroundColor Gray }
}

function Invoke-Start {
    Write-Step "Starting ($(if ($Daemon) { 'daemon' } else { 'foreground' }))..."
    Ensure-BinDir

    if (-not (Test-Path $Binary)) {
        Write-Err "$Binary not found — build first: make build"
        exit 1
    }
    if (-not (Test-Path $Config)) {
        Write-Err "$Config not found"
        exit 1
    }

    # Clean stale pid file.
    if (Test-Path $PidFile) {
        $stale = Get-Content $PidFile -Raw
        Write-Warn "Stale pid file found (pid $stale), removing."
        Remove-Item $PidFile -Force
    }

    if ($Daemon) {
        # --- daemon mode: detached background process ---
        $proc = Start-Process -FilePath $Binary `
            -ArgumentList "-config", $Config `
            -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput $StdoutLog -RedirectStandardError $StderrLog

        Start-Sleep -Seconds 3

        if ($proc.HasExited) {
            Write-Err "linkcode exited immediately (code $($proc.ExitCode))."
            Write-Host "  Stderr tail:" -ForegroundColor Yellow
            if (Test-Path $StderrLog) {
                Get-Content $StderrLog -Tail 15 | ForEach-Object { Write-Host "    $_" -ForegroundColor Red }
            }
            exit 1
        }

        $proc.Id | Out-File -FilePath $PidFile -NoNewline
        Write-OK "Started (pid $($proc.Id))."
        Write-Host "  stdout: $StdoutLog" -ForegroundColor Gray
        Write-Host "  stderr: $StderrLog" -ForegroundColor Gray
        Write-Host "  admin:  http://127.0.0.1:18980" -ForegroundColor Gray
    }
    else {
        # --- foreground mode: logs to console, Ctrl+C to stop ---
        Write-Host "  Running in foreground. Press Ctrl+C to stop." -ForegroundColor Gray
        Write-Host "  admin: http://127.0.0.1:18980" -ForegroundColor Gray
        Write-Host ""
        & $Binary -config $Config
    }
}

function Invoke-Run {
    Invoke-Stop
    Invoke-Build
    Invoke-Start
}

function Invoke-Restart {
    Invoke-Stop
    Invoke-Start
}

function Invoke-Status {
    $procId = Get-PidFromFile
    if ($procId -and (Test-ProcessAlive $procId)) {
        Write-Host "Running (pid $procId)." -ForegroundColor Green
        $proc = Get-Process -Id $procId
        Write-Host "  StartTime : $($proc.StartTime)"
        Write-Host "  CPU       : $([math]::Round($proc.TotalProcessorTime.TotalSeconds, 1))s"
        Write-Host "  Memory    : $([math]::Round($proc.WorkingSet64 / 1MB, 1)) MB"
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
    Remove-Item -Force -ErrorAction SilentlyContinue $StdoutLog
    Remove-Item -Force -ErrorAction SilentlyContinue $StderrLog
    Write-OK "Done."
}

# ============================================================================
# dispatch
# ============================================================================

switch ($Target) {
    "build"   { Invoke-Build }
    "run"     { Invoke-Run }
    "restart" { Invoke-Restart }
    "stop"    { Invoke-Stop }
    "status"  { Invoke-Status }
    "clean"   { Invoke-Clean }
}
