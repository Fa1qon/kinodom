package ru.kinodom.app.core

import java.net.URI

// Route — куда ведёт ссылка из пульта.
enum class Route {
    Inside,  // страница пульта — в приложении
    Outside, // плейлист, поток, чужой сайт, другая схема — Android (VLC, браузер)
    Intent,  // intent:// пульта на Android (VLC с местом и запасным адресом) — разобрать в приложении
}

// Links — ссылки из пульта в приложении: фильмы — в VLC, как в браузере телефона (спека этапа 13, раздел 2),
// «На трекере» — в браузере; внутри — только страницы своего сервера.
object Links {
    fun route(url: String, base: String): Route {
        if (url.startsWith("intent:", ignoreCase = true)) return Route.Intent
        val u = parse(url) ?: return Route.Outside
        val b = parse(base) ?: return Route.Outside
        if (!u.scheme.equals("http", true) || !sameServer(u, b)) return Route.Outside
        val path = u.rawPath.orEmpty()
        if (path.startsWith("/m3u/") || path.startsWith("/stream/") || path.endsWith(".m3u8") || path.endsWith(".m3u")) return Route.Outside
        return Route.Inside
    }

    // fatalError — ошибка загрузки — это «Kinodom не отвечает» только у главной страницы своего сервера; картинки
    // и чужие сайты — нет.
    fun fatalError(isMainFrame: Boolean, url: String, base: String): Boolean {
        if (!isMainFrame) return false
        val u = parse(url) ?: return false
        val b = parse(base) ?: return false
        return sameServer(u, b)
    }

    private fun parse(s: String): URI? = try {
        URI(s)
    } catch (e: Exception) {
        null
    }

    private fun port(u: URI) = if (u.port >= 0) u.port else 80

    private fun sameServer(u: URI, b: URI) = u.host.equals(b.host, true) && port(u) == port(b)
}
