# Сборка bin\kinodom.exe без CGO. Если есть vendor\, Go берёт зависимости оттуда — сеть не нужна.
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'
$version = (git describe --tags --always --dirty 2>$null)
if (-not $version) { $version = 'dev' }
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags "-X main.version=$version" -o bin\kinodom.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "bin\kinodom.exe ($version)"
