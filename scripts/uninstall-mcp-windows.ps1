[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not available.' }
$InstallDir = Join-Path $env:LOCALAPPDATA 'ScopeNest\MCP'
$InstalledBinary = Join-Path $InstallDir 'scopenest-mcp.exe'

try {
    if (Test-Path -LiteralPath $InstalledBinary -PathType Leaf) {
        Remove-Item -LiteralPath $InstalledBinary -Force
    }
    if (Test-Path -LiteralPath $InstallDir -PathType Container) {
        $remaining = @(Get-ChildItem -LiteralPath $InstallDir -Force)
        if ($remaining.Count -eq 0) { Remove-Item -LiteralPath $InstallDir -Force }
    }
    Write-Host 'ScopeNest MCP executable removed. Containers, certificates, proxies, templates, and metadata were preserved.' -ForegroundColor Green
    Write-Host 'Remove the scopenest entry from each MCP client configuration separately.'
} catch {
    Write-Error "ScopeNest MCP removal failed: $($_.Exception.Message)"
    exit 1
}
