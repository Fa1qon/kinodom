# Сборка bin\kinodom.exe и bin\kinodomw.exe без CGO. Если есть vendor\, Go берёт зависимости оттуда —
# сеть не нужна. kinodomw.exe — то же приложение без консоли: ссылки kinodom:// из браузера
# открываются без мелькания окна, ошибки — окном Windows (спека этапа 11a, раздел 4.6).
# -Installer — ещё установщик dist\Kinodom-<версия>-setup.exe (Inno Setup 6:
# winget install JRSoftware.InnoSetup).
param([switch]$Installer)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$env:CGO_ENABLED = '0'

# Версия одна на всё: номер из VERSION и метка git (kinodom version, установщик, «Состояние»).
$base = (Get-Content VERSION -TotalCount 1).Trim()
$rev = (git describe --always --dirty 2>$null)
$version = if ($rev) { "$base-$rev" } else { $base }
$numeric = "$base.0" # Inno Setup: VersionInfoVersion — только числа

New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags "-X main.version=$version" -o bin\kinodom.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go build -trimpath -ldflags "-H windowsgui -X main.version=$version -X main.gui=1" -o bin\kinodomw.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "bin\kinodom.exe, bin\kinodomw.exe ($version)"

if ($Installer) {
    $iscc = @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
        "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
    # Строки — латиницей: PowerShell 5.1 читает файл без BOM как cp1251, кириллица в строке ломает разбор.
    if (-not $iscc) { throw 'Inno Setup 6 not found: winget install JRSoftware.InnoSetup' }
    & $iscc /Q "/DAppVersion=$version" "/DNumVersion=$numeric" installer\kinodom.iss
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "dist\Kinodom-$version-setup.exe"
}
