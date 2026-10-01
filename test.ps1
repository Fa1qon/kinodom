# go vet, gofmt и все тесты проекта. Без CGO — так же, как собирается kinodom.exe.
# Папки spikes\ — отдельные модули Go, в ./... они не попадают.
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$unformatted = gofmt -l cmd internal web
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($unformatted) {
    Write-Host 'gofmt: not formatted:'
    $unformatted | ForEach-Object { Write-Host "  $_" }
    exit 1
}
go test ./... @args
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Тесты приложения для Android на JVM (спека этапа 13, раздел 7): нет JDK 17 или Android SDK — пропуск.
$jdk = if ($env:JAVA_HOME) { $env:JAVA_HOME } else { [Environment]::GetEnvironmentVariable('JAVA_HOME', 'User') }
$sdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME } else { [Environment]::GetEnvironmentVariable('ANDROID_HOME', 'User') }
if (-not ($jdk -and $sdk -and (Test-Path "$jdk\bin\java.exe") -and (Test-Path "$sdk\platforms"))) {
    Write-Host 'JDK 17 or Android SDK not found (JAVA_HOME, ANDROID_HOME) - Android tests skipped'
    exit 0
}
$env:JAVA_HOME, $env:ANDROID_HOME = $jdk, $sdk
Push-Location (Join-Path $PSScriptRoot 'android')
$ErrorActionPreference = 'Continue' # Gradle пишет предупреждения в поток ошибок — судим по коду выхода
& .\gradlew.bat testDebugUnitTest --console=plain -q
$rc = $LASTEXITCODE
Pop-Location
if ($rc -eq 0) { Write-Host 'android: unit tests ok' }
exit $rc
