<#
    Build a portable QuickDrop installer and (optionally) code-sign it.

    PRIMARY FLOW (free, fully supported):
      .\scripts\build-release.ps1
    Builds "build\bin\QuickDrop Setup.exe" (NSIS installer with WebView2
    embedded). Users run the installer ONCE, then open QuickDrop from their
    Start menu. No certs, no batch files, nothing else for the user to do.

    OPTIONAL SIGNING (only if you buy an OV code-signing cert later, to remove
    even the one-time "run anyway" prompt for strangers):
      .\scripts\build-release.ps1 -CertThumbprint "<SHA1 thumbprint>"
      .\scripts\build-release.ps1 -PfxPath "C:\keys\quickdrop.pfx" -PfxPassword "..."
      .\scripts\build-release.ps1 -SkipSign        # build only, no signing

    OUTPUT
      build\bin\QuickDrop Setup.exe   (NSIS installer)
      build\bin\QuickDrop.exe         (the raw app, inside the installer too)

    NOTE: uses `wails build -nsis -webview2 embed` so users' PCs don't need a
    separate WebView2 runtime or network download.
#>
param(
    [string]$CertThumbprint = "",
    [string]$PfxPath = "",
    [string]$PfxPassword = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

# makensis is not on PATH by default even when NSIS is installed; the Wails CLI
# needs it on PATH to build the installer.
$nsis = "${env:ProgramFiles(x86)}\NSIS"
if (Test-Path $nsis) { $env:Path += ";$nsis" }

# Locate the Wails CLI (it ships to %USERPROFILE%\go\bin, which may not be on PATH).
$wails = Get-Command wails -ErrorAction SilentlyContinue
if (-not $wails) {
    $candidate = Join-Path $env:USERPROFILE "go\bin\wails.exe"
    if (Test-Path $candidate) { $wails = Get-Item $candidate }
}
if (-not $wails) { throw "wails CLI not found. Install: go install github.com/wailsapp/wails/v2/cmd/wails@latest" }

if (-not $CertThumbprint -and -not $PfxPath) {
    $SkipSign = $true
} else {
    $SkipSign = $false
}

# ---------------------------------------------------------- build installer
Write-Host "==> Building QuickDrop installer (NSIS + embedded WebView2)..." -ForegroundColor Cyan
& $wails.Source build -nsis -webview2 embed
if ($LASTEXITCODE -ne 0) { throw "wails build failed (exit $LASTEXITCODE)" }

# Wails 2.15 names the installer "<app>-<arch>-installer.exe"; present it as a
# stable, friendly file name for users and for the signing step.
$installer = Get-ChildItem "build\bin\*-installer.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $installer) { throw "no *-installer.exe produced by wails build" }
$final = Join-Path $installer.DirectoryName "QuickDrop Setup.exe"
if ($installer.FullName -ne $final) { Move-Item -Force $installer.FullName $final }

Write-Host ""
if ($SkipSign) {
    Write-Host "==> Build done (unsigned). Users will see one 'More info -> Run anyway'" -ForegroundColor Yellow
    Write-Host "    on first install (this is normal/free). See RELEASING.md for details."
} else {
    Write-Host "==> Signing installer..." -ForegroundColor Cyan
    if ($CertThumbprint) {
        & "$Root\scripts\sign.ps1" -InstallerPath $final -CertThumbprint $CertThumbprint
    } else {
        & "$Root\scripts\sign.ps1" -InstallerPath $final -PfxPath $PfxPath -PfxPassword $PfxPassword
    }
    if ($LASTEXITCODE -ne 0) { throw "signing failed" }
}

Write-Host ""
Write-Host "Done. Give users:" -ForegroundColor Green
Write-Host "    build\bin\QuickDrop Setup.exe"
Write-Host "    (one-line instruction: run it, click Yes, then open QuickDrop from Start)"
