package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Media — что в файле: длительность, видео, озвучки, субтитры (спека 18, 3.2).
type Media struct {
	Duration float64
	BitRate  int64 // бит/с всего файла; 0 — неизвестно
	Video    Video
	Audio    []Track
	Subs     []Sub
}

// Video — дорожка видео; Mime — для браузера ("" — не покажет).
type Video struct {
	ID     int    `json:"-"`
	Codec  string `json:"codec"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Track — озвучка: ID — номер дорожки в файле.
type Track struct {
	ID       int    `json:"id"`
	Lang     string `json:"lang"`
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Default  bool   `json:"default"`
}

// Sub — субтитры: встроенные (ID — номер дорожки) или файл рядом с видео (ID — f0, f1…, File — путь).
type Sub struct {
	ID     string `json:"id"`
	Lang   string `json:"lang"`
	Title  string `json:"title"`
	Image  bool   `json:"image"` // PGS, VobSub — браузеру не отдаются
	Forced bool   `json:"forced"`
	Codec  string `json:"-"`
	File   string `json:"-"`
}

// imageSubs — субтитры-картинки.
var imageSubs = map[string]bool{"hdmv_pgs_subtitle": true, "dvd_subtitle": true, "dvb_subtitle": true, "xsub": true}

var errNoVideo = errors.New("в файле нет видео")

type probeJSON struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		Profile     string            `json:"profile"`
		Level       int               `json:"level"`
		PixFmt      string            `json:"pix_fmt"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Channels    int               `json:"channels"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// tagOf — метка без учёта регистра (MKV пишет LANGUAGE, MP4 — language); «und» — пусто.
func tagOf(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) && v != "und" {
			return v
		}
	}
	return ""
}

// parseProbe — ответ `ffprobe -of json -show_format -show_streams`.
func parseProbe(b []byte) (Media, error) {
	var p probeJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return Media{}, fmt.Errorf("ответ ffprobe не читается: %w", err)
	}
	m := Media{Video: Video{ID: -1}}
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	m.BitRate, _ = strconv.ParseInt(p.Format.BitRate, 10, 64)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if m.Video.ID >= 0 || s.Disposition["attached_pic"] == 1 {
				continue
			}
			m.Video = Video{ID: s.Index, Codec: s.CodecName, Mime: videoMime(s.CodecName, s.Profile, s.Level, s.PixFmt), Width: s.Width, Height: s.Height}
		case "audio":
			m.Audio = append(m.Audio, Track{ID: s.Index, Lang: tagOf(s.Tags, "language"), Title: tagOf(s.Tags, "title"), Codec: s.CodecName,
				Channels: s.Channels, Default: s.Disposition["default"] == 1})
		case "subtitle":
			m.Subs = append(m.Subs, Sub{ID: strconv.Itoa(s.Index), Lang: tagOf(s.Tags, "language"), Title: tagOf(s.Tags, "title"),
				Image: imageSubs[s.CodecName], Forced: s.Disposition["forced"] == 1, Codec: s.CodecName})
		}
	}
	if m.Video.ID < 0 {
		return Media{}, errNoVideo
	}
	return m, nil
}

// Probe — сведения о файле input (адрес на этом сервере или путь).
func (t Tools) Probe(ctx context.Context, input string) (Media, error) {
	out, err := t.output(ctx, t.FFprobe, "-v", "error", "-of", "json", "-show_format", "-show_streams", input)
	if err != nil {
		return Media{}, err
	}
	return parseProbe(out)
}
