package ru.kinodom.app.core

import org.json.JSONObject

// NowTitles — «что идёт сейчас» для списка поверх видео (спека этапа 13, 4.3) из GET /api/v1/channels: название
// передачи по ключу версии и по ключу канала.
object NowTitles {
    fun parse(json: String): Map<String, String> = try {
        val arr = JSONObject(json).getJSONArray("channels")
        val out = HashMap<String, String>()
        for (i in 0 until arr.length()) {
            val c = arr.optJSONObject(i) ?: continue
            val title = c.optJSONObject("now")?.optString("title", "").orEmpty()
            if (title.isEmpty()) continue
            c.optString("version", "").takeIf { it.isNotEmpty() }?.let { out[it] = title }
            c.optString("key", "").takeIf { it.isNotEmpty() }?.let { out.putIfAbsent(it, title) }
        }
        out
    } catch (e: Exception) {
        emptyMap()
    }

    fun titleFor(m: Map<String, String>, ch: Ch): String = m[ch.version] ?: m[ch.key].orEmpty()
}
