# Ключ подписи приложения Kinodom для Android (спека этапа 13, раздел 6). Создаётся один раз на ПК сборки и
# хранится вне репозитория: %USERPROFILE%\.kinodom\kinodom-release.jks и kinodom-release.properties (пароль).
# Ключ — в резервную копию: без него новую версию не поставить поверх старой на телевизорах и телефонах.
# Строки — латиницей: PowerShell 5.1 читает файл без BOM как cp1251.
$ErrorActionPreference = 'Stop'
$dir = Join-Path $env:USERPROFILE '.kinodom'
$jks = Join-Path $dir 'kinodom-release.jks'
$props = Join-Path $dir 'kinodom-release.properties'
$hasJks, $hasProps = (Test-Path $jks), (Test-Path $props)
if ($hasJks -and $hasProps) {
    Write-Host "Signing key exists: $jks"
    exit 0
}
if ($hasJks -or $hasProps) {
    # Половина ключа — не создавать новый поверх: старый подписывал установленные приложения.
    throw "Signing key is incomplete in $dir (need kinodom-release.jks and kinodom-release.properties) - restore it from backup"
}
if (-not $env:JAVA_HOME) { throw 'JAVA_HOME is not set (JDK 17)' }
$keytool = Join-Path $env:JAVA_HOME 'bin\keytool.exe'
New-Item -ItemType Directory -Force $dir | Out-Null
$bytes = New-Object byte[] 24
[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
$pass = [Convert]::ToBase64String($bytes) -replace '[+/=]', 'x'
& $keytool -genkeypair -keystore $jks -storetype PKCS12 -alias kinodom -keyalg RSA -keysize 4096 -validity 36500 `
    -storepass $pass -keypass $pass -dname 'CN=Kinodom, O=Kinodom, C=RU' 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) { throw "keytool exit code $LASTEXITCODE" }
$store = $jks -replace '\\', '/'
@("storeFile=$store", "storePassword=$pass", 'keyAlias=kinodom', "keyPassword=$pass") | Set-Content -Encoding ascii $props
Write-Host "Signing key created: $jks"
Write-Host "Back up the folder $dir - without it updates cannot be installed over the current app."
