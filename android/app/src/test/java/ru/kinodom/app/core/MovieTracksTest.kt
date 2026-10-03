package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Подписи и выбор дорожек (план 18В) — те же правила, что у плеера в браузере (план 18Б).
class MovieTracksTest {
    private val a = listOf(MovieTrack(1, "rus", "Дубляж", "ac3", 6, true), MovieTrack(2, "rus", "LostFilm", "eac3", 6, false), MovieTrack(3, "eng", "", "aac", 2, false))

    @Test
    fun labels() {
        assertEquals("Русский — Дубляж", MovieTracks.label("rus", "Дубляж", 0))
        assertEquals("Английский", MovieTracks.label("eng", "", 2))
        assertEquals("Русский", MovieTracks.label("ru", "Русский", 0))
        assertEquals("FIN", MovieTracks.label("fin", "", 0))
        assertEquals("Дорожка 4", MovieTracks.label("", "", 3))
    }

    @Test
    fun audio() {
        assertEquals(1, MovieTracks.pickAudio(a, null)?.id)
        assertEquals(2, MovieTracks.pickAudio(a, Mem.of("LostFilm", "rus"))?.id)
        assertEquals(3, MovieTracks.pickAudio(a, Mem.of("Кубик", "eng"))?.id)
        assertNull(MovieTracks.pickAudio(emptyList(), null))
    }

    @Test
    fun subs() {
        val s = listOf(MovieSub("3", "rus", "Надписи", false), MovieSub("4", "eng", "Full", true), MovieSub("f0", "eng", "a.eng.srt", false))
        assertNull(MovieTracks.pickSub(s, null, true))
        assertNull(MovieTracks.pickSub(s, Mem.OFF, true))
        assertEquals("4", MovieTracks.pickSub(s, Mem.of("Full", "eng"), true)?.id)
        assertEquals("f0", MovieTracks.pickSub(s, Mem.of("Full", "eng"), false)?.id) // картинки недоступны — по языку из текстовых
        assertEquals("3", MovieTracks.pickSub(s, Mem.of("", "rus"), true)?.id)
    }

    @Test
    fun memory() {
        assertEquals(Mem.of("Дубляж", "rus"), Mem.decode(Mem.of("Дубляж", "rus").encode()))
        assertEquals(Mem.OFF, Mem.decode(Mem.OFF.encode()))
        assertNull(Mem.decode(null))
        assertNull(Mem.decode("мусор"))
    }

    // Живая проверка 18Б: две дорожки «Русский» без названий неразличимы — подписи с кодеком и каналами, память с кодеком.
    @Test
    fun sameLabels() {
        val polden = listOf(MovieTrack(1, "rus", "", "ac3", 6, true), MovieTrack(2, "rus", "", "eac3", 6, false))
        assertEquals(listOf("Русский · AC3 5.1", "Русский · E-AC3 5.1"), MovieTracks.labels(polden))
        assertEquals(listOf("Русский · AC3 5.1 · 1", "Русский · AC3 5.1 · 2"), MovieTracks.labels(listOf(polden[0], polden[0].copy(id = 3))))
        assertEquals(listOf("Русский — Дубляж", "Русский — LostFilm", "Английский"), MovieTracks.labels(a))
        assertEquals(2, MovieTracks.pickAudio(polden, Mem.of("", "rus", "eac3"))?.id)
        assertEquals(Mem.of("", "rus", "eac3"), Mem.decode(Mem.of("", "rus", "eac3").encode()))
        val subs = listOf(MovieSub("3", "rus", "", false), MovieSub("4", "rus", "", false))
        assertEquals(listOf("Русский · 1", "Русский · 2"), MovieTracks.subLabels(subs))
    }
}
