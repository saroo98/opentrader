# Adds the SARO chart template to OpenTrader: it then shows in Chart settings > Template, and in the chart's
# right-click menu > Chart template. It is not made the default.
#
#   irm https://raw.githubusercontent.com/saroo98/opentrader/main/templates/saro/install.ps1 | iex
#
# OpenTrader must be closed while it runs (it keeps its settings in memory and would write over the change).
# A copy of the settings file is kept next to it first. Works with Windows PowerShell 5.1 and PowerShell 7.
param([string]$StorePath = (Join-Path $env:APPDATA 'com.opentrader.app\opentrader.json'))

$ErrorActionPreference = 'Stop'
$templateUrl = 'https://raw.githubusercontent.com/saroo98/opentrader/main/templates/saro/saro.json'
$key = 'ot:chart-settings-templates'

function Stop-WithMessage([string]$message) {
    Write-Host $message -ForegroundColor Yellow
    return
}

if (-not (Test-Path $StorePath)) {
    Stop-WithMessage "OpenTrader's settings were not found ($StorePath). Open OpenTrader once, close it, then run this again."
    return
}
if (Get-Process -Name opentrader -ErrorAction SilentlyContinue) {
    Stop-WithMessage 'Close OpenTrader first (it would write over the new template), then run this again.'
    return
}

# The template: next to this script when it is run from a download, else from GitHub.
$local = if ($PSScriptRoot) { Join-Path $PSScriptRoot 'saro.json' } else { '' }
if ($local -and (Test-Path $local)) {
    $templateJson = [IO.File]::ReadAllText($local)
} else {
    $templateJson = (Invoke-WebRequest -UseBasicParsing -Uri $templateUrl).Content
}
$template = $templateJson | ConvertFrom-Json
if ($template.name -ne 'SARO' -or -not $template.draft) { throw 'The SARO template file is not readable.' }

$storeText = [IO.File]::ReadAllText($StorePath)
$store = $storeText | ConvertFrom-Json

# Each store value is itself JSON text: the templates are a JSON list inside that text.
$list = @()
if ($store.PSObject.Properties[$key] -and $store.$key) {
    $list = @($store.$key | ConvertFrom-Json | Where-Object { $_.name -ne 'SARO' })
}
$list += $template
$value = ConvertTo-Json -InputObject @($list) -Depth 40 -Compress

if ($store.PSObject.Properties[$key]) {
    $store.$key = $value
} else {
    $store | Add-Member -NotePropertyName $key -NotePropertyValue $value
}

$backup = "$StorePath.before-saro-$(Get-Date -Format yyyyMMdd-HHmmss)"
Copy-Item $StorePath $backup
$utf8 = New-Object System.Text.UTF8Encoding $false  # no BOM: the app reads plain UTF-8 JSON
[IO.File]::WriteAllText($StorePath, (ConvertTo-Json -InputObject $store -Depth 5), $utf8)

Write-Host 'SARO template added.' -ForegroundColor Green
Write-Host 'Open OpenTrader: Chart settings (gear) > Template > SARO, then Ok or "Apply to all".'
Write-Host "Previous settings saved as $backup"
