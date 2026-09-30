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
exit $LASTEXITCODE
