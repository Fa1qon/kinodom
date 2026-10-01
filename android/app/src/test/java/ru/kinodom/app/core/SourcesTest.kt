package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Источники канала из GET /api/v1/channels/{версия}/play (спека этапа 13, 4.2): по порядку, с заголовками.
class SourcesTest {
    @Test
    fun parses() {
        val json = """{"items":[{"url":"http://a/1.m3u8","headers":{"userAgent":"UA","referrer":"http://r/"},"title":"Первый","kind":"hls","quality":"1080p"},
            {"url":"","kind":"live"},{"url":"http://b/2","headers":{},"kind":"live"},{"url":"http://c/3.mpd","kind":"dash"}],"title":"Первый","m3uUrl":"x","launchUrl":null}"""
        assertEquals(
            listOf(Source("http://a/1.m3u8", "UA", "http://r/", "hls"), Source("http://b/2", "", "", "live"), Source("http://c/3.mpd", "", "", "dash")),
            Sources.parse(json),
        )
    }

    @Test
    fun garbageIsEmpty() {
        assertEquals(emptyList<Source>(), Sources.parse("не JSON"))
        assertEquals(emptyList<Source>(), Sources.parse("""{"error":"у канала сейчас нет работающих источников"}"""))
    }

    // Без своего User-Agent — тот, с которым сервер проверял источник (probe.UserAgent): иные источники
    // отказывают «ExoPlayerLib».
    @Test
    fun userAgent() {
        assertEquals("UA", Sources.userAgent(Source("u", "UA", "", "hls")))
        assertEquals("VLC/3.0.20 LibVLC/3.0.20", Sources.userAgent(Source("u", "", "", "hls")))
    }

    @Test
    fun mime() {
        assertEquals("application/x-mpegURL", Sources.mime("hls"))
        assertEquals("application/dash+xml", Sources.mime("dash"))
        assertNull(Sources.mime("live"))
        assertNull(Sources.mime(""))
    }
}
