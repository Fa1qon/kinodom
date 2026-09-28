# go vet и все тесты проекта. Без CGO — так же, как собирается kinodom.exe.
# Папки spikes\ — отдельные модули Go, в ./... они не попадают.
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'
go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
go test ./... @args
exit $LASTEXITCODE
