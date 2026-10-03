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

    var player: PlayerMode
        get() = PlayerMode.of(p.getString("player", null)) ?: PlayerMode.Builtin
        set(v) = p.edit().putString("player", v.id).apply()

    var sounds: Boolean
        get() = p.getBoolean("sounds", true)
        set(v) = p.edit().putBoolean("sounds", v).apply()

    // moviePlayer — плеер фильмов (план 18В): встроенный (по умолчанию) или VLC, как раньше.
    var moviePlayer: PlayerMode
        get() = PlayerMode.of(p.getString("moviePlayer", null)) ?: PlayerMode.Builtin
        set(v) = p.edit().putString("moviePlayer", v.id).apply()

    // trackMem — выбор озвучки (kind = "audio") или субтитров ("subs") для раздачи hash.
    fun trackMem(kind: String, hash: String): Mem? = Mem.decode(p.getString("track.$kind.$hash", null))

    fun setTrackMem(kind: String, hash: String, mem: Mem) = p.edit().putString("track.$kind.$hash", mem.encode()).apply()
}
