package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Дорожки Media3 → дорожки сервера (ревью 18В, C1): с внешними субтитрами MergingMediaSource переписывает id форматов в
// «<номер источника>:<id>» — внешний «f0» становится «1:f0», встроенные — «0:3».
class TrackMapTest {
    private val subs = listOf(MovieSub("3", "rus", "Надписи", false), MovieSub("4", "eng", "", true), MovieSub("f0", "rus", "P.rus.srt", false))

    @Test
    fun externalWithPrefix() {
        val ids = listOf("0:3", "0:4", "1:f0")
        assertEquals(2, TrackMap.textGroup(ids, subs, subs[2]))
        assertEquals(0, TrackMap.textGroup(ids, subs, subs[0]))
        assertEquals(1, TrackMap.textGroup(ids, subs, subs[1]))
    }

    @Test
    fun withoutPrefix() {
        val ids = listOf("3", "4", "f0")
        assertEquals(2, TrackMap.textGroup(ids, subs, subs[2]))
        assertEquals(1, TrackMap.textGroup(ids, subs, subs[1]))
    }

    @Test
    fun missing() {
        assertNull(TrackMap.textGroup(listOf("0:3", "0:4"), subs, subs[2]))
        assertNull(TrackMap.textGroup(listOf("1:f0"), subs, subs[0]))
    }

    // Ревью 18В, I3: видео, которое ТВ не декодирует, Media3 просто не выбирает — звук на чёрном экране.
    @Test
    fun video() {
        assertTrue(TrackMap.videoOk(listOf(false, true)))
        assertFalse(TrackMap.videoOk(listOf(false)))
        assertFalse(TrackMap.videoOk(emptyList()))
    }

    // Ревью 18В, I6: текст ошибки сервера — человеку.
    @Test
    fun serverError() {
        assertEquals("Мало места на диске", TrackMap.errorText("""{"error":"Мало места на диске"}"""))
        assertNull(TrackMap.errorText("не json"))
        assertNull(TrackMap.errorText(null))
    }
}
