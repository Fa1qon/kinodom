package ru.kinodom.app.core

import org.json.JSONObject

// Mem — что запомнено о выборе дорожки для раздачи (как у плеера в браузере, план 18Б): название, язык и кодек (дорожки
// одного языка без названий различает только он — живая проверка 18Б) или «выключены».
data class Mem(val title: String, val lang: String, val off: Boolean, val codec: String = "") {
    fun encode(): String = JSONObject().put("title", title).put("lang", lang).put("off", off).put("codec", codec).toString()

    companion object {
        val OFF = Mem("", "", true)

        fun of(title: String, lang: String, codec: String = "") = Mem(title, lang, false, codec)

        fun decode(s: String?): Mem? = try {
            if (s == null) null else JSONObject(s).let { Mem(it.optString("title"), it.optString("lang"), it.optBoolean("off"), it.optString("codec")) }
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

    // pickAudio — по памяти: то же название, затем язык и кодек, затем язык; иначе главная, иначе первая.
    fun pickAudio(ts: List<MovieTrack>, mem: Mem?): MovieTrack? {
        if (ts.isEmpty()) return null
        return mem?.title?.takeIf { it.isNotEmpty() }?.let { m -> ts.firstOrNull { it.title == m } }
            ?: mem?.takeIf { it.lang.isNotEmpty() && it.codec.isNotEmpty() }?.let { m -> ts.firstOrNull { it.lang == m.lang && it.codec == m.codec } }
            ?: mem?.lang?.takeIf { it.isNotEmpty() }?.let { m -> ts.firstOrNull { it.lang == m } }
            ?: ts.firstOrNull { it.default } ?: ts.first()
    }

    private val codecs = mapOf("ac3" to "AC3", "eac3" to "E-AC3", "dts" to "DTS", "truehd" to "TrueHD", "aac" to "AAC", "mp3" to "MP3",
        "flac" to "FLAC", "opus" to "Opus", "vorbis" to "Vorbis", "alac" to "ALAC")
    private val channels = mapOf(1 to "1.0", 2 to "2.0", 6 to "5.1", 8 to "7.1")

    // labels — подписи озвучек: одинаковые — с кодеком и каналами («Русский · AC3 5.1»), всё ещё одинаковые — с номером.
    fun labels(ts: List<MovieTrack>): List<String> = distinct(ts.mapIndexed { i, t -> label(t.lang, t.title, i) }) { i ->
        listOfNotNull(codecs[ts[i].codec] ?: ts[i].codec.uppercase().takeIf { it.isNotEmpty() }, channels[ts[i].channels]).joinToString(" ")
    }

    // subLabels — подписи субтитров: одинаковые — с номером.
    fun subLabels(ss: List<MovieSub>): List<String> = distinct(ss.mapIndexed { i, s -> label(s.lang, s.title, i) }) { "" }

    private fun distinct(base: List<String>, extra: (Int) -> String): List<String> {
        val withExtra = base.mapIndexed { i, l -> if (base.count { it == l } > 1 && extra(i).isNotEmpty()) "$l · ${extra(i)}" else l }
        return withExtra.mapIndexed { i, l -> if (withExtra.count { it == l } > 1) "$l · ${i + 1}" else l }
    }

    // pickSub — без памяти и «выключены» — null; imageOk — субтитры-картинки доступны (прямо — да, запасной путь — нет).
    fun pickSub(ss: List<MovieSub>, mem: Mem?, imageOk: Boolean): MovieSub? {
        if (mem == null || mem.off) return null
        val ok = ss.filter { imageOk || !it.image }
        return mem.title.takeIf { it.isNotEmpty() }?.let { m -> ok.firstOrNull { it.title == m } }
            ?: mem.lang.takeIf { it.isNotEmpty() }?.let { m -> ok.firstOrNull { it.lang == m } }
    }
}
