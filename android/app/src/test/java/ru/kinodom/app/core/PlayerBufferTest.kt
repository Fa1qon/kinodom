package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

class PlayerBufferTest {
    @Test
    fun vodBufferKeepsSeveralMinutesAhead() {
        val b = PlayerBufferPolicy.vod()
        assertEquals(60_000, b.minBufferMs)
        assertEquals(300_000, b.maxBufferMs)
        assertEquals(10_000, b.bufferForPlaybackMs)
        assertEquals(20_000, b.bufferForPlaybackAfterRebufferMs)
    }
}
