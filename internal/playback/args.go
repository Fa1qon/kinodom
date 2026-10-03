package playback

import (
	"math"
	"strconv"
)

// seekPad — запас после ключевого кадра: при B-кадрах ffmpeg ищет на 3/23 с раньше -ss (fftools/ffmpeg_demux.c,
// dts_heuristic) и с -ss ровно на кадре встал бы на предыдущий, а звук начал бы с -ss (замер 2026-10-03: 2 с).
const seekPad = 0.15

// Burst — сколько секунд фильма поток отдаёт сразу; дальше — в темпе просмотра (-readrate 1): иначе плеер
// затянул бы фильм в память целиком (замер 2026-10-03: 700 МБ за секунды). Тесты меняют.
var Burst = 60

// burstBytes — сколько отдать сразу: MSE браузера держит около 150 МБ видео вперёд, а mpegts.js при переполнении
// останавливает загрузку насовсем (ревью 18Б, C1) — запас считается по объёму, а не по секундам.
const burstBytes = 64 << 20

// burstFor — запас потока в секундах для файла с битрейтом bits: около 64 МБ, не меньше 10 с и не больше Burst.
func burstFor(bits int64) int {
	if bits <= 0 {
		return Burst
	}
	sec := int(math.Round(float64(burstBytes) * 8 / float64(bits)))
	return max(10, min(sec, Burst))
}

// seekAt — куда ffmpeg ищет для потока с ключевого кадра from (0 — без поиска). Время потока начинается с кадра
// from: ffmpeg сдвигает всё на запас, чтобы кадр не оказался раньше нуля.
func seekAt(from float64) float64 {
	if from > 0 {
		return from + seekPad
	}
	return 0
}

// streamOpts — поток плеера: ts — браузеру (mpegts.js), mkv — приложению (субтитры — внутри).
type streamOpts struct {
	Input  string
	From   float64 // ключевой кадр, с
	Video  int
	Audio  *Track // nil — в файле нет звука
	Sub    *Sub   // только mkv: текстовые субтитры; у файла рядом реплики сервер шлёт в stdin (WebVTT, сдвинутые)
	Format string // "ts" или "mkv"
	Burst  int    // запас, с (burstFor); 0 — Burst
}

func pace(burst int) []string {
	if burst <= 0 {
		burst = Burst
	}
	return []string{"-readrate", "1", "-readrate_initial_burst", strconv.Itoa(burst)}
}

// streamArgs — ffmpeg: с ключевого кадра, видео как есть, звук — AAC стерео (AAC стерео — как есть).
func streamArgs(o streamOpts) []string {
	a := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, pace(o.Burst)...)
	if at := seekAt(o.From); at > 0 {
		a = append(a, "-ss", secs(at))
	}
	a = append(a, "-i", o.Input)
	sub := o.Sub
	if o.Format != "mkv" {
		sub = nil
	}
	// Файл рядом — не вторым входом с -ss: тот начинает с реплики до места, и ffmpeg сдвигает весь поток, чтобы
	// она была с нуля (замер 2026-10-03: кадр k — на 1 с). Реплики сервер отбирает и сдвигает сам (writeVTT).
	if sub != nil && sub.File != "" {
		a = append(a, "-f", "webvtt", "-i", "pipe:0")
	}
	a = append(a, "-map", "0:"+strconv.Itoa(o.Video))
	if o.Audio != nil {
		a = append(a, "-map", "0:"+strconv.Itoa(o.Audio.ID))
	}
	if sub != nil {
		if sub.File != "" {
			a = append(a, "-map", "1:0")
		} else {
			a = append(a, "-map", "0:"+sub.ID)
		}
	}
	a = append(a, "-c:v", "copy")
	if o.Audio != nil {
		if o.Audio.Codec == "aac" && o.Audio.Channels > 0 && o.Audio.Channels <= 2 {
			a = append(a, "-c:a", "copy")
		} else {
			a = append(a, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
		}
	}
	if sub != nil {
		if c := sub.Codec; sub.File == "" && (c == "subrip" || c == "ass" || c == "ssa") {
			a = append(a, "-c:s", "copy")
		} else {
			a = append(a, "-c:s", "subrip") // кодер «srt» в сборке не включён — только «subrip»
		}
	}
	format := "mpegts"
	if o.Format == "mkv" {
		format = "matroska"
	}
	return append(a, "-f", format, "pipe:1")
}

// subsArgs — встроенные субтитры id браузеру: WebVTT с секунды from в темпе просмотра, время реплик — время файла
// (-copyts): с -ss ffmpeg сдвинул бы реплики так, чтобы первая была с нуля; начало потока вычтет плеер.
func subsArgs(input string, from float64, id string) []string {
	a := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, pace(0)...)
	if from > 0 {
		a = append(a, "-ss", secs(from))
	}
	return append(a, "-copyts", "-i", input, "-map", "0:"+id, "-c:s", "webvtt", "-f", "webvtt", "pipe:1")
}
