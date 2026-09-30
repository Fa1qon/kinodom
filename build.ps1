# Сборка bin\kinodom.exe и bin\kinodomw.exe без CGO. Если есть vendor\, Go берёт зависимости оттуда —
# сеть не нужна. kinodomw.exe — то же приложение без консоли: ссылки kinodom:// из браузера
# открываются без мелькания окна, ошибки — окном Windows (спека этапа 11a, раздел 4.6).
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'
$version = (git describe --tags --always --dirty 2>$null)
if (-not $version) { $version = 'dev' }
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags "-X main.version=$version" -o bin\kinodom.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go build -trimpath -ldflags "-H windowsgui -X main.version=$version -X main.gui=1" -o bin\kinodomw.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "bin\kinodom.exe, bin\kinodomw.exe ($version)"
