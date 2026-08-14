[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [ValidatePattern('^[a-p]{32}$')]
    [string]$ExtensionId = 'hehggadaopoacecdllhhajmbjkdcmajg'
)

$ErrorActionPreference = 'Stop'

$PolicyKey = 'HKCU:\Software\Policies\Google\Chrome'
$PolicyName = 'ExtensionSettings'
$UpdateUrl = 'https://clients2.google.com/service/update2/crx'

if (-not (Test-Path -LiteralPath $PolicyKey)) {
    Write-Host 'ChatGPT browser control policy is not configured.'
    return
}

$property = Get-ItemProperty -LiteralPath $PolicyKey -Name $PolicyName -ErrorAction SilentlyContinue
$raw = $property.$PolicyName
if ([string]::IsNullOrWhiteSpace($raw)) {
    Write-Host 'ChatGPT browser control policy is not configured.'
    return
}

try {
    $settings = $raw | ConvertFrom-Json
}
catch {
    throw "Chrome ExtensionSettings contains invalid JSON. Refusing to modify the existing policy."
}
if ($null -eq $settings -or $settings -isnot [pscustomobject]) {
    throw "Chrome ExtensionSettings is not a JSON object. Refusing to modify the existing policy."
}

$propertyEntry = $settings.PSObject.Properties[$ExtensionId]
if ($null -eq $propertyEntry) {
    Write-Host 'ChatGPT browser control policy is not configured.'
    return
}
$entry = $propertyEntry.Value
if ($entry.installation_mode -ne 'normal_installed' -or $entry.update_url -ne $UpdateUrl) {
    throw "The ChatGPT extension has a different administrator policy. Refusing to remove it."
}

$settings.PSObject.Properties.Remove($ExtensionId)
$removed = $false
if ($PSCmdlet.ShouldProcess($PolicyKey, "Remove ScopeNest ChatGPT extension policy for $ExtensionId")) {
    if ($settings.PSObject.Properties.Count -eq 0) {
        Remove-ItemProperty -LiteralPath $PolicyKey -Name $PolicyName
    }
    else {
        $updated = $settings | ConvertTo-Json -Compress -Depth 32
        Set-ItemProperty -LiteralPath $PolicyKey -Name $PolicyName -Value $updated
    }
    $removed = $true
}

if ($removed) {
    Write-Host 'ScopeNest ChatGPT browser control policy was removed.'
    Write-Host 'Other Chrome extension policies were preserved. Chrome may keep the already-installed extension until you remove it from chrome://extensions.'
}
