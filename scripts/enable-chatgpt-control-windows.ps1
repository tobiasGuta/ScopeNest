[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [ValidatePattern('^[a-p]{32}$')]
    [string]$ExtensionId = 'hehggadaopoacecdllhhajmbjkdcmajg'
)

$ErrorActionPreference = 'Stop'

$PolicyKey = 'HKCU:\Software\Policies\Google\Chrome'
$PolicyName = 'ExtensionSettings'
$UpdateUrl = 'https://clients2.google.com/service/update2/crx'

function Read-ExtensionSettings {
    if (-not (Test-Path -LiteralPath $PolicyKey)) {
        return [pscustomobject]@{}
    }

    $property = Get-ItemProperty -LiteralPath $PolicyKey -Name $PolicyName -ErrorAction SilentlyContinue
    $raw = $property.$PolicyName
    if ([string]::IsNullOrWhiteSpace($raw)) {
        return [pscustomobject]@{}
    }

    try {
        $parsed = $raw | ConvertFrom-Json
    }
    catch {
        throw "Chrome ExtensionSettings contains invalid JSON. Refusing to overwrite the existing policy."
    }
    if ($null -eq $parsed -or $parsed -isnot [pscustomobject]) {
        throw "Chrome ExtensionSettings is not a JSON object. Refusing to overwrite the existing policy."
    }
    return $parsed
}

$settings = Read-ExtensionSettings
$entry = [pscustomobject][ordered]@{
    installation_mode = 'normal_installed'
    update_url = $UpdateUrl
}
$existingEntry = $settings.PSObject.Properties[$ExtensionId]
if ($null -ne $existingEntry) {
    $existingValue = $existingEntry.Value
    if ($existingValue.installation_mode -eq $entry.installation_mode -and $existingValue.update_url -eq $entry.update_url) {
        Write-Host 'ChatGPT browser control policy is already enabled for Google Chrome.'
        return
    }
    throw "The ChatGPT extension already has a different administrator policy. Refusing to overwrite it."
}
$settings | Add-Member -NotePropertyName $ExtensionId -NotePropertyValue $entry -Force
$updated = $settings | ConvertTo-Json -Compress -Depth 32

$applied = $false
if ($PSCmdlet.ShouldProcess($PolicyKey, "Enable normal installation of ChatGPT extension $ExtensionId")) {
    New-Item -Path $PolicyKey -Force | Out-Null
    New-ItemProperty -Path $PolicyKey -Name $PolicyName -PropertyType String -Value $updated -Force | Out-Null
    $applied = $true
}

if ($applied) {
    Write-Host 'ChatGPT browser control policy is enabled for Google Chrome.'
    Write-Host 'Close all Chrome windows, then launch a ScopeNest Chrome container so Chrome can install the extension in that isolated profile.'
    Write-Host 'The policy uses normal_installed mode: the extension is installed automatically but remains user-disableable.'
}
