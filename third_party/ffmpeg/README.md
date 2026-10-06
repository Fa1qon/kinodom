# ffmpeg для своего плеера

Урезанные `ffmpeg.exe` и `ffprobe.exe` для плеера фильмов в браузере и в приложении. Сервер ищет их рядом с `kinodom.exe`; `build.ps1` кладёт их в `bin\`, установщик — в папку программы.

- Версия: FFmpeg 8.1.3.
- Исходники: <https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz>, SHA-256 `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3`.
- Лицензия: LGPL 2.1 или новее ([LICENSE.LGPLv2.1.txt](LICENSE.LGPLv2.1.txt)). Сборка без `--enable-gpl` и `--enable-nonfree`.
- Размер: `ffmpeg.exe` — 11 649 024 Б, `ffprobe.exe` — 11 426 816 Б.

## Что внутри

Только то, что нужно плееру.

- Чтение: matroska, mov/mp4, avi, mpegts, srt, ass, webvtt; протоколы file, http, pipe, tcp.
- Звук: декодеры AC3, E-AC3, DTS, TrueHD, MP3, MP2, MP1, AAC, FLAC, ALAC, Opus, Vorbis, WMA, PCM; кодеры AAC и Opus.
- Видео: копируется как есть (H.264/HEVC); то, что браузер не показывает (MPEG-4/Xvid в AVI и т. п.), перекодируется в VP8 (libvpx) — до 720p в реальном времени. Декодеры H.264, HEVC, MPEG-4, MSMPEG-4v3, H.263 — для сведений и перекода.
- Субтитры: декодеры SubRip, ASS/SSA, WebVTT, mov_text; кодеры WebVTT, SubRip, ASS.
- Запись: MPEG-TS (браузер), Matroska (приложение), WebM (перекод), WebVTT.

Компоненты сверх ffmpeg — тоже в установке, каждый со своим текстом лицензии:
- libvpx 1.15.0 (VP8/VP9), BSD — [LICENSE.vpx.txt](LICENSE.vpx.txt); исходники: <https://github.com/webmproject/libvpx/archive/refs/tags/v1.15.0.tar.gz>, SHA-256 `e935eded7d81631a538bfae703fd1e293aad1c7fd3407ba00440c95105d2011e`.
- libopus 1.5.2 (Opus), BSD — [LICENSE.opus.txt](LICENSE.opus.txt); исходники: <https://downloads.xiph.org/releases/opus/opus-1.5.2.tar.gz>, SHA-256 `65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1`.

## Пересобрать

Нужен Docker Desktop (или GitHub Actions — workflow `ffmpeg.yml`). Из этой папки в Git Bash:

```sh
MSYS_NO_PATHCONV=1 docker run --rm -v "$(cygpath -w "$PWD"):/out" -v "$(cygpath -w "$PWD/build.sh"):/build.sh:ro" debian:bookworm sh /build.sh
```

Скрипт скачивает исходники, собирает их кросс-компилятором mingw-w64 и кладёт сюда `ffmpeg.exe`, `ffprobe.exe` и текст лицензии. Неверное имя компонента configure пропускает молча, поэтому после configure скрипт проверяет, что каждый компонент из списков включён. Контрольную сумму исходников он пишет в `source.sha256`: её нужно сверить со строкой выше и удалить файл.
