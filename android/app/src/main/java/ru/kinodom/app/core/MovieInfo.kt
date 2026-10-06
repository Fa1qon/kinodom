package ru.kinodom.app.core

import org.json.JSONObject

// MovieTrack, MovieSub, MovieInfo — сведения о файле для плеера приложения (план 18В): ответ /api/v1/play/{src} (план 18А).
data class MovieTrack(val id: Int, val lang: String, val title: String, val codec: String, val channels: Int, val default: Boolean)

data class MovieSub(val id: String, val lang: String, val title: String, val image: Boolean) {
    val external: Boolean get() = id.startsWith("f")
}

data class MovieInfo(
    val src: String,
    val title: String,
    val hash: String,
    val index: Int,
    val durationSec: Double,
    val startSec: Int,
    val direct: String,
    val m3uUrl: String,
    val audio: List<MovieTrack>,
    val subs: List<MovieSub>,
    val prevSrc: String?,
    val prevTitle: String?,
    val nextSrc: String?,
    val nextTitle: String?,
) {
    companion object {
        fun parse(json: String): MovieInfo? = try {
            val o = JSONObject(json)
            val a = o.getJSONArray("audio")
            val s = o.getJSONArray("subs")
            val prev = o.optJSONObject("prev")
            val next = o.optJSONObject("next")
            MovieInfo(
                src = o.getString("src"), title = o.getString("title"), hash = o.getString("hash"), index = o.getInt("index"),
                durationSec = o.getDouble("durationSec"), startSec = o.optInt("startSec"), direct = o.getString("direct"),
                m3uUrl = o.optString("m3uUrl"),
                audio = (0 until a.length()).map { i ->
                    val t = a.getJSONObject(i)
                    MovieTrack(t.getInt("id"), t.optString("lang"), t.optString("title"), t.optString("codec"), t.optInt("channels"), t.optBoolean("default"))
                },
                subs = (0 until s.length()).map { i ->
                    val t = s.getJSONObject(i)
                    MovieSub(t.getString("id"), t.optString("lang"), t.optString("title"), t.optBoolean("image"))
                },
                prevSrc = prev?.optString("src")?.takeIf { it.isNotEmpty() },
                prevTitle = prev?.optString("title"),
                nextSrc = next?.optString("src")?.takeIf { it.isNotEmpty() },
                nextTitle = next?.optString("title"),
            )
        } catch (e: Exception) {
            null
        }
    }
}
