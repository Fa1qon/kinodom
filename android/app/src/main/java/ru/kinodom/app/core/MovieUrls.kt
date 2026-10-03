package ru.kinodom.app.core

import java.util.Locale

// MovieUrls — адреса плеера фильмов на сервере (план 18А) и тело отчёта места.
object MovieUrls {
    private val srcRe = Regex("^(torrent/[0-9a-fA-F]{40}/\\d+|library/\\d+)$")

    fun validSrc(s: String) = srcRe.matches(s)

    // info, keyframe, history — пути после /api/v1/ (Api.get).
    fun info(src: String, fromStart: Boolean) = "play/$src" + if (fromStart) "?fromStart=1" else ""

    fun keyframe(src: String, at: Double) = "play/$src/keyframe?t=${at.toLong()}"

    fun history(hash: String, index: Int) = "history/$hash/$index"

    private fun t3(x: Double): String {
        val r = Math.round(x * 1000) / 1000.0
        return if (r == Math.floor(r)) r.toLong().toString() else r.toString()
    }

    // stream — поток сервера с кадра k: Matroska, озвучка audio, субтитры внутри (sub).
    fun stream(base: String, src: String, k: Double, audio: Int?, sub: String?, sid: String): String =
        "${base}play/$src/stream.mkv?t=${t3(k)}&sid=$sid" + (audio?.let { "&a=$it" } ?: "") + (sub?.let { "&s=$it" } ?: "")

    // subs — внешние субтитры целиком (время файла) — для прямого пути.
    fun subs(base: String, src: String, id: String, sid: String) = "${base}play/$src/subs/$id.vtt?t=0&sid=$sid"

    fun report(pos: Double, dur: Double): String {
        val p = Math.round(pos.coerceIn(0.0, dur) * 10) / 10.0
        return String.format(Locale.ROOT, "{\"positionSec\":%.1f,\"durationSec\":%.1f}", p, dur)
    }

    // nearEnd — поток кончился у конца файла (последние 30 с), а не оборвался.
    fun nearEnd(pos: Double, dur: Double) = dur > 0 && pos >= dur - 30

    fun newSid(rand: () -> Double = Math::random): String {
        val abc = "abcdefghijklmnopqrstuvwxyz0123456789"
        return (1..16).map { abc[(rand() * abc.length).toInt().coerceIn(0, abc.length - 1)] }.joinToString("")
    }

    // vlcUri — исходный файл для VLC: без own=1 (место по чтению угадывает сервер, как раньше).
    fun vlcUri(direct: String): String {
        val q = direct.indexOf('?')
        if (q < 0) return direct
        val rest = direct.substring(q + 1).split('&').filter { it.isNotEmpty() && it != "own=1" }
        return direct.substring(0, q) + if (rest.isEmpty()) "" else "?" + rest.joinToString("&")
    }
}
