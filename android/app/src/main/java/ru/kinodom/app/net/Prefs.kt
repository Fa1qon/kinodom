package ru.kinodom.app.net

import android.content.Context
import ru.kinodom.app.core.PlayerMode

// Prefs — запомненный адрес пульта (спека этапа 13, раздел 3.2) и плеер каналов (раздел 2 «Системный плеер»).
class Prefs(context: Context) {
    private val p = context.applicationContext.getSharedPreferences("kinodom", Context.MODE_PRIVATE)

    var base: String?
        get() = p.getString("base", null)
        set(v) = p.edit().putString("base", v).apply()

    var player: PlayerMode
        get() = PlayerMode.of(p.getString("player", null)) ?: PlayerMode.Builtin
        set(v) = p.edit().putString("player", v.id).apply()
}
