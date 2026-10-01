package ru.kinodom.app.core

import org.json.JSONObject

// Source — источник канала из GET /api/v1/channels/{версия}/play (спека этапа 13, 4.2): адрес, заголовки
// User-Agent и Referer (пусто — не слать), вид потока по проверке сервера (hls, dash, live — MPEG-TS по HTTP).
data class Source(val url: String, val userAgent: String, val referrer: String, val kind: String)

object Sources {
    // UA — с ним сервер проверяет источники (internal/iptv/probe, UserAgent): с «ExoPlayerLib» иные отказывают.
    const val UA = "VLC/3.0.20 LibVLC/3.0.20"

    fun userAgent(s: Source): String = s.userAgent.ifEmpty { UA }

    // parse — источники по порядку; без адреса — пропуск; мусор или ошибка сервера — пусто.
    fun parse(json: String): List<Source> = try {
        val arr = JSONObject(json).optJSONArray("items")
        val out = ArrayList<Source>()
        if (arr != null) {
            for (i in 0 until arr.length()) {
                val o = arr.optJSONObject(i) ?: continue
                val url = o.optString("url", "")
                if (url.isEmpty()) continue
                val h = o.optJSONObject("headers")
                out += Source(url, h?.optString("userAgent", "").orEmpty(), h?.optString("referrer", "").orEmpty(), o.optString("kind", ""))
            }
        }
        out
    } catch (e: Exception) {
        emptyList()
    }

    // mime — тип для Media3: HLS и DASH — явно (адрес может быть без расширения); поток — по содержимому.
    fun mime(kind: String): String? = when (kind) {
        "hls" -> "application/x-mpegURL"
        "dash" -> "application/dash+xml"
        else -> null
    }
}
