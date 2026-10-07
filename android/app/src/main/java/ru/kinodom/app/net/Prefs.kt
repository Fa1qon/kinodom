package ru.kinodom.app.net

import android.content.Context
import ru.kinodom.app.core.Mem
import ru.kinodom.app.core.PlayerMode

// Prefs — запомненный адрес пульта (спека этапа 13, раздел 3.2), плеер каналов (раздел 2 «Системный плеер») и звуки
// меню (план 16В).
class Prefs(context: Context) {
    private val p = context.applicationContext.getSharedPreferences("kinodom", Context.MODE_PRIVATE)

    var base: String?
        get() = p.getString("base", null)
        set(v) = p.edit().putString("base", v).apply()

    // localServer — сервер на этом устройстве (полный порт, план 2026-10-06). Пока человек не
    // выбирал («auto»): с сервером в сети не знакомы (адрес не сохранён) — стартуют свой, знакомы —
    // как раньше, по сети. Явное on/off перекрывает.
    var localServer: Boolean
        get() = p.getString("localServer", "auto") != "off"
        set(v) = p.edit().putString("localServer", if (v) "on" else "off").apply()

    val localServerAuto: Boolean
        get() = "auto" == p.getString("localServer", "auto")

    var player: PlayerMode
        get() = PlayerMode.of(p.getString("player", null)) ?: PlayerMode.Builtin
        set(v) = p.edit().putString("player", v.id).apply()

    // Unified player mode for movies and channels.
    var playbackMode: PlayerMode
        get() = PlayerMode.of(p.getString("playbackMode", null))
            ?: PlayerMode.of(p.getString("moviePlayer", null))
            ?: PlayerMode.of(p.getString("player", null))
            ?: PlayerMode.Builtin
        set(v) = p.edit().putString("playbackMode", v.id).putString("player", v.id).putString("moviePlayer", v.id).apply()

    var moviePlaybackMode: PlayerMode
        get() = PlayerMode.of(p.getString("moviePlaybackMode", null))
            ?: PlayerMode.of(p.getString("moviePlayer", null))
            ?: PlayerMode.Builtin
        set(v) = p.edit().putString("moviePlaybackMode", v.id).putString("moviePlayer", v.id).apply()

    var channelPlaybackMode: PlayerMode
        get() = PlayerMode.of(p.getString("channelPlaybackMode", null))
            ?: PlayerMode.of(p.getString("playbackMode", null))
            ?: PlayerMode.of(p.getString("player", null))
            ?: PlayerMode.Builtin
        set(v) = p.edit().putString("channelPlaybackMode", v.id).putString("playbackMode", v.id).putString("player", v.id).apply()
    var sounds: Boolean
        get() = p.getBoolean("sounds", false)
        set(v) = p.edit().putBoolean("sounds", v).apply()

    // moviePlayer — плеер фильмов (план 18В): встроенный (по умолчанию) или VLC, как раньше.
    var moviePlayer: PlayerMode
        get() = PlayerMode.of(p.getString("moviePlayer", null)) ?: PlayerMode.Builtin
        set(v) = p.edit().putString("moviePlayer", v.id).apply()

    // trackMem — выбор озвучки (kind = "audio") или субтитров ("subs") для раздачи hash.
    fun trackMem(kind: String, hash: String): Mem? = Mem.decode(p.getString("track.$kind.$hash", null))

    fun setTrackMem(kind: String, hash: String, mem: Mem) = p.edit().putString("track.$kind.$hash", mem.encode()).apply()
}
