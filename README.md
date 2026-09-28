# Kinodom

Домашний медиасервер: торренты, IPTV, медиатека, DLNA — одна служба Windows для телевизоров в доме.

- Спека: `docs/superpowers/specs/2026-09-28-kinodom-server-design.md`
- План: `docs/superpowers/plans/2026-09-28-kinodom-00-roadmap.md`
- Проверки перед планом: `docs/research/2026-09-28-spikes.md`

## Сборка и тесты

    .\build.ps1   # bin\kinodom.exe
    .\test.ps1    # go vet + все тесты

Зависимости лежат в `vendor/`, сеть для сборки не нужна.
