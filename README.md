# Kinodom

## Требования

- Windows 10 или 11 (x64). Microsoft Edge нужен для входа на Rutracker.
- Для сборки: Go 1.26; Inno Setup 6 — для установщика; JDK 17 и Android SDK (платформа 36) — для приложения.
- Node.js — только для тестов пульта.

## Сборка и установка

```powershell
.\build.ps1              # bin\kinodom.exe и bin\kinodomw.exe; если есть JDK и Android SDK — ещё bin\kinodom.apk
.\build.ps1 -Installer   # и dist\Kinodom-<версия>-setup.exe
.\test.ps1               # go vet, gofmt, тесты Go и пульта, тесты приложения
```

Зависимости Go лежат в `vendor/`: сеть для сборки сервера не нужна. Приложение подписывается ключом из `%USERPROFILE%\.kinodom` (создаётся при первой сборке, `android\make-key.ps1`). Сохраните эту папку: без ключа новую версию приложения не поставить поверх старой.

После установки пульт открывается по адресу `http://<адрес ПК>:8090`. Приложение для ТВ и телефона скачивается оттуда же: `http://<адрес ПК>:8090/app`.

Команды сервера (полный список — `kinodom` без аргументов): `run` (в консоли), `install` / `uninstall` (служба), `check` (проверка установки), `version`.

## Устройство

| Папка | Что внутри |
|---|---|
| `cmd/kinodom` | точка входа: служба, консоль, установка, проверка, значок в трее |
| `internal/` | модули сервера под сторожем: `torrents`, `catalog`, `source` (rutor, rutracker, jacred), `meta` (Кинопоиск), `iptv`, `library`, `discovery` (SSDP), `appdist` (раздача APK), `api`, `setup`, `store` (SQLite) |
| `web/static` | пульт — ES-модули без сборки |
| `android` | приложение для Android (Kotlin, Media3) |
| `installer` | установщик Inno Setup |

## Версии

Номера — по [SemVer](https://semver.org/lang/ru/): исправления меняют третью цифру, новые возможности — вторую. Каждый выпуск — тег `vX.Y.Z` и запись в [CHANGELOG.md](CHANGELOG.md); номер выпуска — в файле `VERSION`. Сборка на теге называется `0.13.0`, между выпусками — `0.13.0-<коммитов после тега>-g<хэш>`.

## Лицензия

MIT — см. [LICENSE](LICENSE).
