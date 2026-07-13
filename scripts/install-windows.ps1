[CmdletBinding()]
param(
    [ValidatePattern('^[a-p]{32}$')]
    [string]$ExtensionId = 'nnmpnmnmmfoedjeionoopgnbjnepfolh',
    [string]$BinaryPath,
    [ValidateSet('Chrome', 'Edge', 'Brave')]
    [string[]]$Browsers = @('Chrome', 'Edge', 'Brave')
)

$ErrorActionPreference = 'Stop'
$HostName = 'com.scopenest.host'
$RepoRoot = Split-Path -Parent $PSScriptRoot
$InstallDir = Join-Path $env:LOCALAPPDATA 'ScopeNest\NativeHost'
$InstalledBinary = Join-Path $InstallDir 'scopenest-host.exe'
$ManifestPath = Join-Path $InstallDir "$HostName.json"

try {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

    if ($BinaryPath) {
        $sourceBinary = (Resolve-Path -LiteralPath $BinaryPath).Path
    } else {
        $goCommand = Get-Command go -ErrorAction SilentlyContinue
        $goPath = if ($goCommand) { $goCommand.Source } else { $null }
        if (-not $goPath) {
			$installedGo = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
			if (Test-Path -LiteralPath $installedGo) { $goPath = $installedGo }
		}
		if (-not $goPath) {
            throw 'Go 1.22 or newer was not found. Install Go, or pass -BinaryPath to a prebuilt scopenest-host.exe.'
        }
        $sourceDir = Join-Path $RepoRoot 'native-host'
        Push-Location $sourceDir
        try {
			& $goPath build -buildvcs=false -trimpath -ldflags '-s -w' -o $InstalledBinary './cmd/scopenest-host'
            if ($LASTEXITCODE -ne 0) { throw 'The native host build failed.' }
        } finally {
            Pop-Location
        }
        $sourceBinary = $InstalledBinary
    }

    if ($sourceBinary -ne $InstalledBinary) {
        Copy-Item -LiteralPath $sourceBinary -Destination $InstalledBinary -Force
    }
    if (-not (Test-Path -LiteralPath $InstalledBinary -PathType Leaf)) { throw 'The installed native host executable is missing.' }

    $manifest = [ordered]@{
        name = $HostName
        description = 'ScopeNest native messaging companion'
        path = $InstalledBinary
        type = 'stdio'
        allowed_origins = @("chrome-extension://$ExtensionId/")
    }
    $manifestJson = $manifest | ConvertTo-Json -Depth 4
    [System.IO.File]::WriteAllText($ManifestPath, $manifestJson, [System.Text.UTF8Encoding]::new($false))

    $registryPaths = @{
        Chrome = "HKCU:\Software\Google\Chrome\NativeMessagingHosts\$HostName"
        Edge = "HKCU:\Software\Microsoft\Edge\NativeMessagingHosts\$HostName"
        # Brave uses the Chrome-compatible native-messaging registry namespace on Windows.
        Brave = "HKCU:\Software\Google\Chrome\NativeMessagingHosts\$HostName"
    }
    foreach ($browser in $Browsers) {
        $key = $registryPaths[$browser]
        New-Item -Path $key -Force | Out-Null
        Set-Item -Path $key -Value $ManifestPath
        if ((Get-Item -Path $key).GetValue('') -ne $ManifestPath) { throw "Failed to validate $browser native-host registration." }
        Write-Host "Registered ScopeNest for $browser." -ForegroundColor Green
    }

    $parsed = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    if ($parsed.name -ne $HostName -or $parsed.path -ne $InstalledBinary -or $parsed.allowed_origins[0] -ne "chrome-extension://$ExtensionId/") {
        throw 'Native-host manifest validation failed.'
    }
    Write-Host "ScopeNest native host installed at $InstalledBinary" -ForegroundColor Green
    Write-Host "Authorized extension ID: $ExtensionId"
    Write-Host 'Restart the browser, then reopen ScopeNest.'
} catch {
    Write-Error "ScopeNest installation failed: $($_.Exception.Message)"
    exit 1
}
