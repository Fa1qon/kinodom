package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MovieUrlsTest {
    private val hx = "a".repeat(40)

    @Test
    fun src() {
        assertTrue(MovieUrls.validSrc("library/7"))
        assertTrue(MovieUrls.validSrc("torrent/$hx/2"))
        assertFalse(MovieUrls.validSrc("torrent/zz/2"))
        assertFalse(MovieUrls.validSrc("library/7/../1"))
        assertFalse(MovieUrls.validSrc(""))
    }

    @Test
    fun paths() {
        assertEquals("play/library/7", MovieUrls.info("library/7", false))
        assertEquals("play/library/7?fromStart=1", MovieUrls.info("library/7", true))
        assertEquals("play/library/7/keyframe?t=1200", MovieUrls.keyframe("library/7", 1200.6))
        assertEquals("http://h:8090/play/library/7/stream.mkv?t=1196.04&sid=abc&a=2&s=f0",
            MovieUrls.stream("http://h:8090/", "library/7", 1196.04, 2, "f0", "abc"))
        assertEquals("http://h:8090/play/library/7/stream.mkv?t=0&sid=abc", MovieUrls.stream("http://h:8090/", "library/7", 0.0, null, null, "abc"))
        assertEquals("http://h:8090/play/library/7/subs/f0.vtt?t=0&sid=abc", MovieUrls.subs("http://h:8090/", "library/7", "f0", "abc"))
        assertEquals("history/lib-1/7", MovieUrls.history("lib-1", 7))
        assertEquals("""{"positionSec":1200.0,"durationSec":2559.0}""", MovieUrls.report(1200.04, 2559.0))
        assertEquals("""{"positionSec":2559.0,"durationSec":2559.0}""", MovieUrls.report(3000.0, 2559.0))
        assertEquals("http://h:8090/media/7/P.mkv", MovieUrls.vlcUri("http://h:8090/media/7/P.mkv?own=1"))
        assertEquals("http://h:8090/m.mkv?x=1", MovieUrls.vlcUri("http://h:8090/m.mkv?own=1&x=1"))
    }

    @Test
    fun endAndSid() {
        assertTrue(MovieUrls.nearEnd(2540.0, 2559.0))
        assertFalse(MovieUrls.nearEnd(1200.0, 2559.0))
        assertFalse(MovieUrls.nearEnd(10.0, 0.0))
        val sid = MovieUrls.newSid { 0.5 }
        assertEquals(16, sid.length)
        assertTrue(sid.all { it.isLetterOrDigit() })
    }
}
