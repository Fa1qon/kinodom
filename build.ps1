# Сборка bin\kinodom.exe и bin\kinodomw.exe без CGO. Если есть vendor\, Go берёт зависимости оттуда —
# сеть не нужна. kinodomw.exe — то же приложение без консоли: ссылки kinodom:// из браузера
# открываются без мелькания окна, ошибки — окном Windows (спека этапа 11a, раздел 4.6).
# -Installer — ещё установщик dist\Kinodom-<версия>-setup.exe (Inno Setup 6:
# winget install JRSoftware.InnoSetup). Есть JDK 17 и Android SDK (JAVA_HOME, ANDROID_HOME) — ещё приложение для
# Android: bin\kinodom.apk и bin\kinodom.apk.json (версия и номер сборки — спека этапа 13, раздел 6).
param([switch]$Installer)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$env:CGO_ENABLED = '0'

# Версия одна на всё: номер из VERSION и метка git (kinodom version, установщик, «Состояние»).
$base = (Get-Content VERSION -TotalCount 1).Trim()
# Выпуск — коммит с тегом v<VERSION> без правок: «0.13.0»; между выпусками — метка после номера:
# «0.13.0-3-g1a2b3c4» (3 коммита после тега), «0.13.0-dirty» — с несохранёнными правками; тегов нет — хэш.
$desc = (git describe --tags --match 'v*' --dirty --always 2>$null)
if ($desc -eq "v$base") { $version = $base }
elseif ($desc -match '^v[\d.]+-(.+)$') { $version = "$base-$($Matches[1])" }
elseif ($desc) { $version = "$base-$desc" }
else { $version = $base }
$numeric = "$base.0" # Inno Setup: VersionInfoVersion — только числа

New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags "-X main.version=$version" -o bin\kinodom.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go build -trimpath -ldflags "-H windowsgui -X main.version=$version -X main.gui=1" -o bin\kinodomw.exe .\cmd\kinodom
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "bin\kinodom.exe, bin\kinodomw.exe ($version)"
# Полный порт сервера для приложения (план 2026-10-06): android/arm64 в jniLibs как lib*.so — Android
# сам распаковывает в nativeLibraryDir, откуда разрешён запуск. Сборка с пределом кучи по умолчанию
# (GOMEMLIMIT ставит приложение). Сервер есть — есть и ffprobe/ffmpeg android: их же и в jniLibs.
$jni = "android\app\src\main\jniLibs\arm64-v8a"
New-Item -ItemType Directory -Force $jni | Out-Null
$env:GOOS, $env:GOARCH = 'android', 'arm64'
go build -trimpath -ldflags "-s -w -checklinkname=0 -X main.version=$version" -o "$jni\libkinodomserver.so" .\cmd\kinodom
$env:GOOS, $env:GOARCH = '', ''
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
foreach ($t in 'ffprobe', 'ffmpeg') {
    if (Test-Path "third_party\ffmpeg\android\$t") { Copy-Item "third_party\ffmpeg\android\$t" "$jni\lib$t.so" -Force }
}
Write-Host "$jni\libkinodomserver.so ($version)"
# Свой плеер (план 18А): урезанный ffmpeg — рядом с kinodom.exe, там его ищет сервер.
Copy-Item third_party\ffmpeg\ffmpeg.exe, third_party\ffmpeg\ffprobe.exe bin\ -Force

# Приложение для Android: версия — как у сервера, номер сборки — число коммитов; подпись — ключ из
# %USERPROFILE%\.kinodom (make-key.ps1 создаёт его при первой сборке). Нет JDK или SDK — без APK.
foreach ($f in 'bin\kinodom.apk', 'bin\kinodom.apk.json') { if (Test-Path $f) { Remove-Item $f } }
$jdk = if ($env:JAVA_HOME) { $env:JAVA_HOME } else { [Environment]::GetEnvironmentVariable('JAVA_HOME', 'User') }
$sdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME } else { [Environment]::GetEnvironmentVariable('ANDROID_HOME', 'User') }
if ($jdk -and $sdk -and (Test-Path "$jdk\bin\java.exe") -and (Test-Path "$sdk\platforms")) {
    $env:JAVA_HOME, $env:ANDROID_HOME = $jdk, $sdk
    & .\android\make-key.ps1
    # Номер сборки — 1000 + число коммитов: после чистки истории перед публикацией (2026-10-01) коммитов стало
    # меньше, чем номер уже установленных приложений (до 445), а Android ставит обновление, только если номер больше.
    $code = 1000 + [int](git rev-list --count HEAD)
    if (git status --porcelain) { $code++ }
    Push-Location android
    # Свойства — в кавычках: PowerShell режет «-PversionName=0.11.0-…» на два аргумента. Gradle пишет
    # предупреждения в поток ошибок — судим по коду выхода.
    $ErrorActionPreference = 'Continue'
    & .\gradlew.bat assembleRelease "`"-PversionName=$version`"" "`"-PversionCode=$code`"" --console=plain -q
    $rc = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    Pop-Location
    if ($rc -ne 0) { exit $rc }
    Copy-Item android\app\build\outputs\apk\release\app-release.apk bin\kinodom.apk
    [ordered]@{ version = $version; versionCode = $code } | ConvertTo-Json -Compress | Set-Content -Encoding ascii bin\kinodom.apk.json
    Write-Host "bin\kinodom.apk ($version, $code)"
} else {
    Write-Host 'JDK 17 or Android SDK not found (JAVA_HOME, ANDROID_HOME) - no kinodom.apk'
}

if ($Installer) {
    $iscc = @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
        "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
    # Строки — латиницей: PowerShell 5.1 читает файл без BOM как cp1251, кириллица в строке ломает разбор.
    if (-not $iscc) { throw 'Inno Setup 6 not found: winget install JRSoftware.InnoSetup' }
    & $iscc /Q "/DAppVersion=$version" "/DNumVersion=$numeric" installer\kinodom.iss
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "dist\Kinodom-$version-setup.exe"
}
