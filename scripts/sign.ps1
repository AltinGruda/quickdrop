<#
    Sign a Windows binary with an OV or self-signed code-signing cert using the
    Windows SDK signtool. Called by build-release.ps1 when a cert is supplied.

    USAGE
      .\scripts\sign.ps1 -InstallerPath "build\bin\QuickDrop Setup.exe" -CertThumbprint "<SHA1>"
      .\scripts\sign.ps1 -InstallerPath "..." -PfxPath "C:\keys\quickdrop.pfx" -PfxPassword "..."
#>
param(
    [Parameter(Mandatory=$true)][string]$InstallerPath,
    [string]$CertThumbprint = "",
    [string]$PfxPath = "",
    [string]$PfxPassword = ""
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path $InstallerPath)) { throw "Not found: $InstallerPath" }

$signtool = Get-ChildItem "C:\Program Files (x86)\Windows Kits\10\bin" -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue |
    Sort-Object FullName -Descending | Select-Object -First 1
if (-not $signtool) { $signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue }
if (-not $signtool) {
    Write-Warning "signtool.exe not found (Windows SDK). Install: winget install Microsoft.WindowsSDK.10.0.26100"
    throw "signtool.exe not found."
}

$args = @("sign", "/v", "/fd", "sha256", "/td", "sha256", "/tr", "http://timestamp.digicert.com")
if ($CertThumbprint) {
    $args += @("/sha1", $CertThumbprint)
} elseif ($PfxPath) {
    $args += @("/f", $PfxPath)
    if ($PfxPassword) { $args += @("/p", $PfxPassword) }
} else {
    throw "Provide -CertThumbprint or -PfxPath."
}
$args += $InstallerPath

Write-Host "==> signtool sign ..." -ForegroundColor Cyan
& $signtool.Source @args
if ($LASTEXITCODE -ne 0) { throw "signtool sign failed (exit $LASTEXITCODE)" }

Write-Host "==> signtool verify ..." -ForegroundColor Cyan
& $signtool.Source verify /v /pa $InstallerPath
