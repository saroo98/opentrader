# Builds dist\oanda-<version>.zip: the manifest and bin\oanda-plugin.exe. Install it in OpenTrader with
# Settings > Plugins > Install from file.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
go test -count=1 ./...
if ($LASTEXITCODE) { throw 'tests failed' }
$m = Get-Content opentrader-plugin.json -Raw | ConvertFrom-Json
$stage = Join-Path $PSScriptRoot 'build'
Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory "$stage\bin" | Out-Null
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags '-s -w' -o "$stage\bin\oanda-plugin.exe" .
if ($LASTEXITCODE) { throw 'build failed' }
Copy-Item opentrader-plugin.json $stage
New-Item -ItemType Directory dist -Force | Out-Null
$zip = "dist\oanda-$($m.version).zip"
Remove-Item $zip -ErrorAction SilentlyContinue
Compress-Archive -Path "$stage\*" -DestinationPath $zip
Remove-Item $stage -Recurse -Force
"$zip  sha256 $((Get-FileHash $zip -Algorithm SHA256).Hash.ToLower())"
