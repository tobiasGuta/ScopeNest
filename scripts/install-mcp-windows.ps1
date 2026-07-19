[CmdletBinding()]
param(
    [string]$BinaryPath
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not available.' }
$InstallDir = Join-Path $env:LOCALAPPDATA 'ScopeNest\MCP'
$InstalledBinary = Join-Path $InstallDir 'scopenest-mcp.exe'

try {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

    if ($BinaryPath) {
        $source = Get-Item -LiteralPath $BinaryPath -ErrorAction Stop
        if ($source.PSIsContainer) { throw 'BinaryPath must identify a file.' }
        Copy-Item -LiteralPath $source.FullName -Destination $InstalledBinary -Force
    } else {
        $goCommand = Get-Command go -ErrorAction SilentlyContinue
        if (-not $goCommand) { throw 'Go 1.25 or newer is required, or pass -BinaryPath to a prebuilt scopenest-mcp.exe.' }
        $versionText = & $goCommand.Source version
        if ($LASTEXITCODE -ne 0 -or $versionText -notmatch 'go1\.(\d+)') { throw 'Could not determine the installed Go version.' }
        if ([int]$Matches[1] -lt 25) { throw "Go 1.25 or newer is required; found $versionText." }
        Push-Location (Join-Path $RepoRoot 'native-host')
        try {
            & $goCommand.Source build -buildvcs=false -trimpath -ldflags '-s -w' -o $InstalledBinary './cmd/scopenest-mcp'
            if ($LASTEXITCODE -ne 0) { throw 'The ScopeNest MCP build failed.' }
        } finally {
            Pop-Location
        }
    }

    if (-not (Test-Path -LiteralPath $InstalledBinary -PathType Leaf)) { throw 'The installed MCP executable is missing.' }

    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $acl = Get-Acl -LiteralPath $InstallDir
    $acl.SetAccessRuleProtection($true, $false)
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $identity,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
        [System.Security.AccessControl.PropagationFlags]::None,
        [System.Security.AccessControl.AccessControlType]::Allow
    )
    $acl.SetAccessRule($rule)
    Set-Acl -LiteralPath $InstallDir -AclObject $acl

    Write-Host "ScopeNest MCP installed at $InstalledBinary" -ForegroundColor Green
    Write-Host 'No browser registration or AI-client configuration was changed.'
    Write-Host "Register with Codex: codex mcp add scopenest -- `"$InstalledBinary`""
} catch {
    Write-Error "ScopeNest MCP installation failed: $($_.Exception.Message)"
    exit 1
}
