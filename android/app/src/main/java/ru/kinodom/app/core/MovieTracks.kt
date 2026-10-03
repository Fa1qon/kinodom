package ru.kinodom.app.core

import org.json.JSONObject

// Mem — что запомнено о выборе дорожки для раздачи (как у плеера в браузере, план 18Б): название и язык или «выключены».
data class Mem(val title: String, val lang: String, val off: Boolean) {
    fun encode(): String = JSONObject().put("title", title).put("lang", lang).put("off", off).toString()

    companion object {
        val OFF = Mem("", "", true)

        fun of(title: String, lang: String) = Mem(title, lang, false)

        fun decode(s: String?): Mem? = try {
            if (s == null) null else JSONObject(s).let { Mem(it.optString("title"), it.optString("lang"), it.optBoolean("off")) }
        } catch (e: Exception) {
            null
        }
    }
}

// MovieTracks — подписи и выбор дорожек (план 18В; правила — как в браузере, план 18Б).
object MovieTracks {
    private val langs = mapOf(
        "rus" to "Русский", "ru" to "Русский", "eng" to "Английский", "en" to "Английский", "ukr" to "Украинский", "uk" to "Украинский",
        "jpn" to "Японский", "ja" to "Японский", "ger" to "Немецкий", "deu" to "Немецкий", "de" to "Немецкий", "fre" to "Французский",
        "fra" to "Французский", "fr" to "Французский", "spa" to "Испанский", "es" to "Испанский", "ita" to "Итальянский", "it" to "Итальянский",
        "kor" to "Корейский", "ko" to "Корейский", "chi" to "Китайский", "zho" to "Китайский", "zh" to "Китайский",
    )

    // label — «Русский — Дубляж»; без названия — язык; без языка — название; без того и другого — «Дорожка N».
    fun label(lang: String, title: String, i: Int): String {
        val l = langs[lang] ?: lang.uppercase()
        val t = title.trim()
        return when {
            l.isNotEmpty() && t.isNotEmpty() && !t.equals(l, ignoreCase = true) -> "$l — $t"
            l.isNotEmpty() -> l
            t.isNotEmpty() -> t
            else -> "Дорожка ${i + 1}"
        }
    }

    // pickAudio — по памяти: то же название, затем язык; иначе главная, иначе первая.
    fun pickAudio(ts: List<MovieTrack>, mem: Mem?): MovieTrack? {
        if (ts.isEmpty()) return null
        return mem?.title?.takeIf { it.isNotEmpty() }?.let { m -> ts.firstOrNull { it.title == m } }
            ?: mem?.lang?.takeIf { it.isNotEmpty() }?.let { m -> ts.firstOrNull { it.lang == m } }
            ?: ts.firstOrNull { it.default } ?: ts.first()
    }

    // pickSub — без памяти и «выключены» — null; imageOk — субтитры-картинки доступны (прямо — да, запасной путь — нет).
    fun pickSub(ss: List<MovieSub>, mem: Mem?, imageOk: Boolean): MovieSub? {
        if (mem == null || mem.off) return null
        val ok = ss.filter { imageOk || !it.image }
        return mem.title.takeIf { it.isNotEmpty() }?.let { m -> ok.firstOrNull { it.title == m } }
            ?: mem.lang.takeIf { it.isNotEmpty() }?.let { m -> ok.firstOrNull { it.lang == m } }
    }
}
