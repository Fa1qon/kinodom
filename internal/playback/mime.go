package playback

import "fmt"

// h264Profiles — профиль H.264 → profile_idc и constraint-байт строки avc1.
var h264Profiles = map[string]string{"Constrained Baseline": "42E0", "Baseline": "4200", "Main": "4D40", "Extended": "5800", "High": "6400"}

// videoMime — строка кодека для MediaSource.isTypeSupported('video/mp4; codecs="…"'): mpegts.js кладёт поток в
// MSE как MP4. H.264 8 бит — avc1.PPCCLL; HEVC Main и Main 10 — hvc1…; прочее — "" (браузер не покажет).
func videoMime(codec, profile string, level int, pixFmt string) string {
	if level <= 0 {
		return ""
	}
	switch codec {
	case "h264":
		pc, ok := h264Profiles[profile]
		if !ok || (pixFmt != "" && pixFmt != "yuv420p" && pixFmt != "yuvj420p") {
			return ""
		}
		return fmt.Sprintf("avc1.%s%02X", pc, level)
	case "hevc":
		switch {
		case profile == "Main" && (pixFmt == "" || pixFmt == "yuv420p"):
			return fmt.Sprintf("hvc1.1.6.L%d.B0", level)
		case profile == "Main 10" && (pixFmt == "" || pixFmt == "yuv420p10le"):
			return fmt.Sprintf("hvc1.2.4.L%d.B0", level)
		}
	}
	return ""
}
