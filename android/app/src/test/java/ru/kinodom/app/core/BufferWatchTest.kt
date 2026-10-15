package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class BufferWatchTest {
    @Test
    fun startupBufferingIsIgnored() {
        val w = BufferWatch()
        w.onStart(0)
        w.onBuffer(1_000)
        w.onBuffer(2_000)
        w.onBuffer(3_000)
        assertFalse(w.shouldFallback(7_999))
    }

    @Test
    fun repeatedBufferingFallsBackOnce() {
        val w = BufferWatch()
        w.onStart(0)
        w.onBuffer(9_000)
        w.onBuffer(18_000)
        w.onBuffer(27_000)
        assertTrue(w.shouldFallback(27_100))
        assertTrue(w.fired())
        assertFalse(w.shouldFallback(28_000))
    }

    @Test
    fun sparseBufferingDoesNotFallback() {
        val w = BufferWatch()
        w.onStart(0)
        w.onBuffer(9_000)
        w.onBuffer(45_000)
        w.onBuffer(80_000)
        assertFalse(w.shouldFallback(80_100))
    }
}
