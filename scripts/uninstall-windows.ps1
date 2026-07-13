[CmdletBinding(SupportsShouldProcess)]
param(
    [switch]$RemoveData
)

$ErrorActionPreference = 'Stop'
$HostName = 'com.scopenest.host'
$InstallDir = Join-Path $env:LOCALAPPDATA 'ScopeNest\NativeHost'
$keys = @(
    "HKCU:\Software\Google\Chrome\NativeMessagingHosts\$HostName",
    "HKCU:\Software\Microsoft\Edge\NativeMessagingHosts\$HostName"
)

foreach ($key in $keys) {
    if (Test-Path -Path $key) {
        Remove-Item -Path $key -Recurse -Force
        Write-Host "Removed $key"
    }
}
if (Test-Path -LiteralPath $InstallDir) {
    Remove-Item -LiteralPath $InstallDir -Recurse -Force
    Write-Host "Removed $InstallDir"
}
if ($RemoveData) {
    $dataDir = Join-Path $env:APPDATA 'ScopeNest'
    if ($PSCmdlet.ShouldProcess($dataDir, 'Permanently remove all ScopeNest container profiles and metadata')) {
        Remove-Item -LiteralPath $dataDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host 'Removed ScopeNest container data.'
    }
} else {
    Write-Host 'Container data was preserved. Use -RemoveData to remove it explicitly.'
}
Write-Host 'ScopeNest native-host registration removed.' -ForegroundColor Green
