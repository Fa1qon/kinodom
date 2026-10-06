package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Сведения о файле для плеера приложения (план 18В) — ответ /api/v1/play/{src} сервера (план 18А).
class MovieInfoTest {
    private val json = """{"src":"library/7","title":"Полдень — 1×01","hash":"lib-1","index":7,"durationSec":2559.04,"startSec":1384,
        "direct":"http://192.168.0.26:8090/media/7/P.mkv?own=1","m3uUrl":"http://192.168.0.26:8090/m3u/library/7.m3u8","launchUrl":null,
        "video":{"codec":"h264","mime":"avc1.640028","width":1920,"height":804},
        "audio":[{"id":1,"lang":"rus","title":"Дубляж","codec":"ac3","channels":6,"default":true},{"id":2,"lang":"rus","title":"","codec":"eac3","channels":6,"default":false}],
        "subs":[{"id":"3","lang":"rus","title":"Надписи","image":false,"forced":true},{"id":"4","lang":"eng","title":"","image":true,"forced":false},
                {"id":"f0","lang":"rus","title":"P.rus.srt","image":false,"forced":false}],
        "prev":{"src":"library/6","title":"Полдень — 1×00"},
        "next":{"src":"library/8","title":"Полдень — 1×02"}}"""

    @Test
    fun parses() {
        val i = MovieInfo.parse(json)!!
        assertEquals("library/7", i.src)
        assertEquals("lib-1", i.hash)
        assertEquals(7, i.index)
        assertEquals(2559.04, i.durationSec, 0.001)
        assertEquals(1384, i.startSec)
        assertEquals(listOf(MovieTrack(1, "rus", "Дубляж", "ac3", 6, true), MovieTrack(2, "rus", "", "eac3", 6, false)), i.audio)
        assertEquals(listOf("3", "4", "f0"), i.subs.map { it.id })
        assertEquals(true, i.subs[1].image)
        assertEquals("library/6", i.prevSrc)
        assertEquals("Полдень — 1×00", i.prevTitle)
        assertEquals("library/8", i.nextSrc)
        assertEquals("Полдень — 1×02", i.nextTitle)
    }

    @Test
    fun noNeighboursNoAudio() {
        val i = MovieInfo.parse(json.replace(Regex(""""audio":\[.*?],\s*"subs""", RegexOption.DOT_MATCHES_ALL), """"audio":[],"subs""")
            .replace(Regex(""""prev":\{[^}]*},\s*"""), "").replace(Regex(""""next":\{[^}]*}"""), """"next":null"""))!!
        assertEquals(0, i.audio.size)
        assertNull(i.prevSrc)
        assertNull(i.nextSrc)
    }

    @Test
    fun garbage() {
        assertNull(MovieInfo.parse("не json"))
        assertNull(MovieInfo.parse("""{"src":"library/7"}"""))
    }
}
