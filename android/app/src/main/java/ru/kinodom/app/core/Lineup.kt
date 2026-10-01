package ru.kinodom.app.core

import org.json.JSONObject

// Ch — канал списка плеера (спека этапа 13, 5.2): ключ канала, ключ версии («Смотреть», программа), номер
// кнопки (0 — нет), логотип — путь на сервере, подпись версии («МСК+4» — только если версий несколько).
data class Ch(val key: String, val version: String, val name: String, val number: Int, val logo: String, val label: String)

// Lineup — список, из которого открыли канал, и стартовый в нём (мост playChannels).
data class Lineup(val items: List<Ch>, val start: Int, val listName: String) {
    companion object {
        // parse — JSON моста; мусор, пустой список — null. Канал без ключа или имени — пропуск, стартовый остаётся
        // тем же каналом; start вне списка — первый.
        fun parse(json: String): Lineup? = try {
            val o = JSONObject(json)
            val arr = o.getJSONArray("list")
            val want = o.optInt("start", 0)
            val items = ArrayList<Ch>()
            var start = 0
            for (i in 0 until arr.length()) {
                val c = arr.optJSONObject(i) ?: continue
                val key = c.optString("key", "")
                val name = c.optString("name", "")
                if (key.isEmpty() || name.isEmpty()) continue
                if (i == want) start = items.size
                items += Ch(key, c.optString("version", "").ifEmpty { key }, name, c.optInt("number", 0), c.optString("logo", ""), c.optString("label", ""))
            }
            if (items.isEmpty()) null else Lineup(items, start, o.optString("listName", ""))
        } catch (e: Exception) {
            null
        }
    }
}
