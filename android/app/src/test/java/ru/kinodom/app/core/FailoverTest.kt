package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Перебор источников (спека этапа 13, 4.2; план 13b, Review Focus 2): ошибка, 8 с без картинки, конец живого
// потока — следующий; кончились — «Канал сейчас не показывает»; радио (звук без видео) — работает.
class FailoverTest {
    @Test
    fun walksSourcesThenGivesUp() {
        val f = Failover(3)
        assertEquals(0, f.index)
        assertEquals(1, f.onFailed())
        assertEquals(2, f.onFailed())
        assertNull(f.onFailed())
        assertNull(f.onFailed())
    }

    @Test
    fun noSources() = assertNull(Failover(0).onFailed())

    @Test
    fun silence() = assertEquals(8000L, Failover.SILENCE_MS)

    @Test
    fun accepts() {
        assertTrue(Failover.accepts(video = true, audio = true, firstFrame = true))
        assertTrue(Failover.accepts(video = false, audio = true, firstFrame = false)) // радио
        assertFalse(Failover.accepts(video = true, audio = true, firstFrame = false)) // видео есть, кадра нет
        assertFalse(Failover.accepts(video = false, audio = false, firstFrame = false))
    }
}
