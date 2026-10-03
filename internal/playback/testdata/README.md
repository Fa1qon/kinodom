# Тестовые файлы плеера

Сделаны один раз полной сборкой ffmpeg на ПК (winget `Gyan.FFmpeg`). Урезанная сборка из `third_party/ffmpeg` их не сделает: в ней нет кодеров видео.

- `embedded.srt` — три реплики для `sample.mkv`: 1,0–2,5 с «Первая реплика», 5,0–6,5 с «Вторая реплика», 9,0–10,5 с «Третья реплика».
- `sample.rus.srt` — внешние: 3,0–4,0 с «Внешняя один», 7,0–8,0 с «Внешняя два».
- `sample.mkv` — 12 с, ключевые кадры через 2 с. Дорожки:
  - 0 — H.264 High 4.0, 320×180;
  - 1 — AC3 5.1 rus «Дубляж», главная;
  - 2 — AAC стерео eng «Original»;
  - 3 — SRT rus «Надписи».
- `hevc.mp4`, `hi10.mkv`, `xvid.avi` — по 4 с: HEVC Main, H.264 High 10 и MPEG-4 ASP (XviD) с MP3. Браузер покажет только первый, и то не всякий.

```sh
ffmpeg -f lavfi -i testsrc2=size=320x180:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 -f lavfi -i sine=frequency=880:sample_rate=48000 -i embedded.srt -t 12 \
 -map 0:v -map 1:a -map 2:a -map 3:s -c:v libx264 -profile:v high -level 4.0 -pix_fmt yuv420p -g 50 -keyint_min 50 -sc_threshold 0 -b:v 150k \
 -c:a:0 ac3 -ac:a:0 6 -b:a:0 192k -c:a:1 aac -ac:a:1 2 -b:a:1 64k -c:s srt \
 -metadata:s:a:0 language=rus -metadata:s:a:0 "title=Дубляж" -disposition:a:0 default -metadata:s:a:1 language=eng -metadata:s:a:1 title=Original -disposition:a:1 0 \
 -metadata:s:s:0 language=rus -metadata:s:s:0 "title=Надписи" sample.mkv
ffmpeg -f lavfi -i testsrc2=size=320x180:rate=25 -f lavfi -i sine=frequency=440:sample_rate=44100 -t 4 -c:v mpeg4 -vtag XVID -b:v 200k -c:a libmp3lame -b:a 64k xvid.avi
ffmpeg -f lavfi -i testsrc2=size=320x180:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 4 -c:v libx265 -tag:v hvc1 -x265-params log-level=error -b:v 150k -c:a aac -b:a 64k hevc.mp4
ffmpeg -f lavfi -i testsrc2=size=320x180:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 4 -c:v libx264 -profile:v high10 -pix_fmt yuv420p10le -b:v 150k -c:a aac -b:a 64k hi10.mkv
```
