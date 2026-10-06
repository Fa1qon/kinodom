package ru.kinodom.app.core

// TechInfo — строки технической панели плееров (план 2026-10-06, Task B; спека единого плеера, раздел
// «Единый встроенный Android-плеер»: кнопка технической информации). Чистые функции: плееры подставляют
// данные Media3 (sampleMimeType, frameRate, DecoderCounters, буфер), здесь — только вид строк.
object TechInfo {

    // codec — кодек человеком: mimeType Media3 («video/avc» → «H.264», «audio/eac3» → «E-AC-3»);
    // неизвестный — последний сегмент капсом, пусто — «—».
    fun codec(mime: String?): String {
        if (mime.isNullOrBlank()) return "—"
        val known = mapOf(
            "video/avc" to "H.264", "video/hevc" to "H.265", "video/mp4v-es" to "MPEG-4",
            "video/x-vnd.vp8" to "VP8", "video/x-vnd.on2.vp9" to "VP9", "video/av01" to "AV1",
            "audio/mp4a-latm" to "AAC", "audio/ac3" to "AC-3", "audio/eac3" to "E-AC-3",
            "audio/dts" to "DTS", "audio/vnd.dts.hd" to "DTS-HD", "audio/true-hd" to "TrueHD",
            "audio/opus" to "Opus", "audio/mpeg" to "MP3", "audio/raw" to "PCM",
        )
        known[mime]?.let { return it }
        return mime.substringAfter('/').uppercase().take(12)
    }

    // fps — «24 FPS» из метаданных формата (23.976 округляется); нечисловое (≤ 0) — «—».
    fun fps(rate: Float): String = if (rate > 0f) "${Math.round(rate)} FPS" else "—"

    // resolution — «1920×804»; кадра нет — «—».
    fun resolution(w: Int, h: Int): String = if (w > 0 && h > 0) "$w×$h" else "—"

    // panel — строки панели: «Видео: H.264 · 1920×804 · 24 FPS», «Звук: AAC», «Буфер: 12 с»,
    // «Пропущено кадров: 12». Нечего показывать — одна строка «Нет данных».
    fun panel(videoMime: String?, w: Int, h: Int, rate: Float, audioMime: String?, bufferSec: Long, dropped: Int): List<String> {
        val out = mutableListOf<String>()
        val v = mutableListOf<String>()
        if (videoMime != null && videoMime.isNotBlank()) v += codec(videoMime)
        if (w > 0 && h > 0) v += resolution(w, h)
        if (rate > 0f) v += fps(rate)
        if (v.isNotEmpty()) out += "Видео: " + v.joinToString(" · ")
        if (audioMime != null && audioMime.isNotBlank()) out += "Звук: " + codec(audioMime)
        if (bufferSec > 0) out += "Буфер: $bufferSec с"
        if (dropped > 0) out += "Пропущено кадров: $dropped"
        return out.ifEmpty { listOf("Нет данных") }
    }
}
