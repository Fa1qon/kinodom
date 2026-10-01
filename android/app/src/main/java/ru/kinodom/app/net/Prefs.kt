package ru.kinodom.app.net

import android.content.Context

// Prefs — запомненный адрес пульта (спека этапа 13, раздел 3.2).
class Prefs(context: Context) {
    private val p = context.applicationContext.getSharedPreferences("kinodom", Context.MODE_PRIVATE)

    var base: String?
        get() = p.getString("base", null)
        set(v) = p.edit().putString("base", v).apply()
}
