package ru.kinodom.app.core

import org.json.JSONObject

// TrackMap — дорожки Media3 → дорожки сервера и что показать человеку (план 18В, ревью 18В).
object TrackMap {
    // baseId — id формата без номера источника: с внешними субтитрами MergingMediaSource пишет «<источник>:<id>»
    // (внешний «f0» становится «1:f0»), без них — id как есть.
    private fun baseId(id: String?): String = id?.substringAfter(':') ?: ""

    // textGroup — номер текстовой группы Media3 для субтитров chosen: внешние — по id («f0»), встроенные — по порядку
    // среди встроенных (k-я встроенная группа — k-я встроенная дорожка сервера); не нашлось — null.
    fun textGroup(groupIds: List<String?>, subs: List<MovieSub>, chosen: MovieSub): Int? {
        if (chosen.external) return groupIds.indexOfFirst { baseId(it) == chosen.id }.takeIf { it >= 0 }
        val external = subs.filter { it.external }.map { it.id }.toSet()
        val embedded = groupIds.indices.filter { baseId(groupIds[it]) !in external }
        return embedded.getOrNull(subs.filter { !it.external }.indexOf(chosen))
    }

    // videoOk — в файле есть видео, которое устройство покажет: иначе Media3 его просто не выбирает, и идёт звук на чёрном.
    fun videoOk(supported: List<Boolean>) = supported.any { it }

    // errorText — текст ошибки из ответа сервера {"error": "…"}; нет — null.
    fun errorText(body: String?): String? = try {
        body?.let { JSONObject(it).optString("error").takeIf { s -> s.isNotEmpty() } }
    } catch (e: Exception) {
        null
    }
}
