# ffmpeg для своего плеера

Урезанные `ffmpeg.exe` и `ffprobe.exe` для плеера фильмов в браузере и в приложении. Сервер ищет их рядом с `kinodom.exe`; `build.ps1` кладёт их в `bin\`, установщик — в папку программы.

- Версия: FFmpeg 8.1.3.
- Исходники: <https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz>, SHA-256 `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`.
- Лицензия: LGPL 2.1 или новее ([LICENSE.LGPLv2.1.txt](LICENSE.LGPLv2.1.txt)). Сборка без `--enable-gpl` и `--enable-nonfree`.
- Размер: `ffmpeg.exe` — 5 069 824 Б, `ffprobe.exe` — 4 852 736 Б.

## Что внутри

Только то, что нужно плееру. Видео не перекодируется, только копируется.

- Чтение: matroska, mov/mp4, avi, mpegts, srt, ass, webvtt; протоколы file, http, pipe, tcp.
- Звук: декодеры AC3, E-AC3, DTS, TrueHD, MP3, MP2, AAC, FLAC, Opus, Vorbis, PCM; кодер AAC.
- Видео: декодеры H.264 и HEVC — только для сведений о файле.
- Субтитры: декодеры SubRip, ASS/SSA, WebVTT, mov_text; кодеры WebVTT, SubRip, ASS.
- Запись: MPEG-TS (браузер), Matroska (приложение), WebVTT.

## Пересобрать

Нужен Docker Desktop. Из этой папки в Git Bash:

```sh
MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -w "$PWD"):/out" -v "$(cygpath -w "$PWD/build.sh"):/build.sh:ro" debian:bookworm sh /build.sh
```

Скрипт скачивает исходники, собирает их кросс-компилятором mingw-w64 и кладёт сюда `ffmpeg.exe`, `ffprobe.exe` и текст лицензии. Контрольную сумму исходников он пишет в `source.sha256`: её нужно сверить со строкой выше и удалить файл.
