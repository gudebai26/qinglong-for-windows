# Builds the single-file panel executable and stages the release folder.
#
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\build.ps1 -Dist release
#
# Output (default dist\):
#   qinglong.exe   panel + React build + vendored node_modules, no runtime deps
#   start.bat      launcher (GBK, CRLF)
#   README.txt     install / migrate / FAQ
#
# node_modules must exist in the repository: it is compiled into the executable
# and extracted to <QL_DIR>\node_modules on first boot (missing files only, an
# existing Node installation is never overwritten).
#
# A running panel locks its executable on Windows, so any qinglong.exe /
# qinglong-go.exe that holds the panel port is stopped before the link step.
[CmdletBinding()]
param(
    [string]$Dist = "dist",
    [int]$Port = 5700,
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $distDir = Join-Path $root $Dist
    $exe = Join-Path $distDir "qinglong.exe"

    # --- preflight ---------------------------------------------------------
    if (-not (Test-Path (Join-Path $root "node_modules"))) {
        throw "node_modules is missing: it is embedded into the executable and extracted on first boot."
    }
    $modules = (Get-ChildItem (Join-Path $root "node_modules") -Recurse -File | Measure-Object).Count
    Write-Host ("node_modules: {0} files will be embedded" -f $modules)

    if (-not $SkipTests) {
        Write-Host "go test ./..."
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
    }

    # --- stop the previous build -------------------------------------------
    # The panel may be running from this very folder (the build overwrites its
    # own executable) or from another one that holds the same port. Both would
    # make the link step fail with "Access is denied".
    function Stop-Panel {
        param([string]$Reason)
        $stopped = 0
        foreach ($name in @("qinglong", "qinglong-go")) {
            Get-Process -Name $name -ErrorAction SilentlyContinue | ForEach-Object {
                Write-Host ("stopping {0} (pid {1}) - {2}" -f $_.ProcessName, $_.Id, $Reason)
                # /T kills the task tree: a scheduled task must not outlive the
                # panel it was started from.
                & taskkill.exe /PID $_.Id /T /F 2>&1 | Out-Null
                $stopped++
            }
        }
        try {
            Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop |
                Select-Object -ExpandProperty OwningProcess -Unique |
                ForEach-Object {
                    if ($_ -gt 4) {
                        Write-Host ("stopping pid {0} listening on {1} - {2}" -f $_, $Port, $Reason)
                        & taskkill.exe /PID $_ /T /F 2>&1 | Out-Null
                        $stopped++
                    }
                }
        } catch {
            # Get-NetTCPConnection is unavailable on very old hosts; the process
            # name check above already covered the normal case.
        }
        if ($stopped -gt 0) {
            for ($i = 0; $i -lt 40; $i++) {
                Start-Sleep -Milliseconds 250
                $busy = $false
                if (Test-Path $exe) {
                    try { $stream = [IO.File]::Open($exe, "Open", "ReadWrite", "None"); $stream.Close() }
                    catch { $busy = $true }
                }
                if (-not $busy) { break }
            }
        }
        return $stopped
    }

    Stop-Panel -Reason "rebuild" | Out-Null
    if (Test-Path $distDir) { Remove-Item $distDir -Recurse -Force }
    New-Item -ItemType Directory -Path $distDir | Out-Null

    # --- build -------------------------------------------------------------
    $env:CGO_ENABLED = "0"
    Write-Host "go build -trimpath -ldflags ""-s -w"" -o $exe ./cmd/qinglong"
    go build -trimpath -ldflags "-s -w" -o $exe ./cmd/qinglong
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    $item = Get-Item $exe
    Write-Host ("built {0} ({1:N1} MB)" -f $item.FullName, ($item.Length / 1MB))

    # --- stage -------------------------------------------------------------
    # start.bat is written as GBK with CRLF: cmd.exe reads batch files with the
    # OEM code page and drops any command that follows a UTF-8 Chinese string.
    $template = Join-Path $PSScriptRoot "start.bat.template"
    $bat = (Get-Content -LiteralPath $template -Raw -Encoding UTF8) -replace "`r?`n", "`r`n"
    $gbk = [System.Text.Encoding]::GetEncoding(936)
    [IO.File]::WriteAllText((Join-Path $distDir "start.bat"), $bat, $gbk)

    $readme = Join-Path $root "docs\README.txt"
    if (Test-Path $readme) {
        # UTF-8 with BOM so Notepad shows the Chinese text correctly.
        [IO.File]::WriteAllText((Join-Path $distDir "README.txt"),
            (Get-Content -LiteralPath $readme -Raw -Encoding UTF8),
            (New-Object System.Text.UTF8Encoding($true)))
    } else {
        Write-Warning "docs\README.txt is missing, the release has no readme"
    }

    foreach ($name in @("start.bat", "README.txt")) {
        $path = Join-Path $distDir $name
        if (Test-Path $path) {
            Write-Host ("staged {0} ({1} bytes)" -f $name, (Get-Item $path).Length)
        }
    }

    $total = (Get-ChildItem $distDir -File | Measure-Object -Property Length -Sum).Sum
    Write-Host ("release ready: {0} ({1:N1} MB total)" -f $distDir, ($total / 1MB))
    Get-ChildItem $distDir | Select-Object Name, Length | Format-Table -AutoSize | Out-String | Write-Host
}
finally {
    Pop-Location
}
