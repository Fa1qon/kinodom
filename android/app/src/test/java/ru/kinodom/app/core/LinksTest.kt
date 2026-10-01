package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// Ссылки из пульта: страницы пульта — внутри; плейлисты и потоки (фильмы — в VLC, спека этапа 13, 2), чужие
// сайты («На трекере»), другие схемы — снаружи; intent:// пульта на Android (VLC с местом) — разобрать в
// приложении. Ошибка загрузки — экран «Kinodom не отвечает» только у главной страницы своего сервера.
class LinksTest {
    private val base = "http://192.168.0.26:8090/"

    @Test
    fun pultPageInside() = assertEquals(Route.Inside, Links.route("http://192.168.0.26:8090/#/catalog/rutor/4", base))

    @Test
    fun rootInside() = assertEquals(Route.Inside, Links.route("http://192.168.0.26:8090/", base))

    @Test
    fun playlistOutside() = assertEquals(Route.Outside, Links.route("http://192.168.0.26:8090/m3u/abcd/0.m3u8", base))

    @Test
    fun channelPlaylistOutside() = assertEquals(Route.Outside, Links.route("http://192.168.0.26:8090/m3u/channel/pervy.m3u8", base))

    @Test
    fun streamOutside() = assertEquals(Route.Outside, Links.route("http://192.168.0.26:8090/stream/abcd/0/film.mkv", base))

    @Test
    fun trackerOutside() = assertEquals(Route.Outside, Links.route("https://rutor.info/torrent/1089193", base))

    @Test
    fun otherServerOutside() = assertEquals(Route.Outside, Links.route("http://192.168.0.26:8097/#/catalog", base))

    @Test
    fun customSchemeOutside() = assertEquals(Route.Outside, Links.route("kinodom://play?url=x", base))

    @Test
    fun androidIntent() = assertEquals(
        Route.Intent,
        Links.route("intent://192.168.0.26:8090/stream/abcd/0/film.mkv#Intent;scheme=http;type=video/*;package=org.videolan.vlc;end", base),
    )

    @Test
    fun mainFrameOfServerIsFatal() = assertEquals(true, Links.fatalError(isMainFrame = true, url = "http://192.168.0.26:8090/", base = base))

    @Test
    fun subresourceIsNot() = assertEquals(false, Links.fatalError(isMainFrame = false, url = "http://192.168.0.26:8090/img/x", base = base))

    @Test
    fun otherHostIsNot() = assertEquals(false, Links.fatalError(isMainFrame = true, url = "https://rutor.info/", base = base))
}
