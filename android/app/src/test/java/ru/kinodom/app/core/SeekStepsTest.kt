package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Перемотка с пульта ТВ (спека 18, 6.3): шаг 10 с первые 2 с удержания, затем 30 с, после 5 с — 60 с; перемотка —
// по отпусканию, одна.
class SeekStepsTest {
    @Test
    fun steps() {
        assertEquals(10, SeekSteps.step(0))
        assertEquals(10, SeekSteps.step(1999))
        assertEquals(30, SeekSteps.step(2000))
        assertEquals(60, SeekSteps.step(5000))
    }

    @Test
    fun singlePress() {
        val h = Hold(1, 100.0, 0)
        assertEquals(110.0, h.target, 0.0)
    }

    @Test
    fun holdGrows() {
        val h = Hold(-1, 1000.0, 0)
        assertFalse(h.repeat(100)) // повторы клавиши чаще 300 мс — без шага
        assertTrue(h.repeat(300))
        assertEquals(980.0, h.target, 0.0)
        h.repeat(2100)
        assertEquals(950.0, h.target, 0.0)
        h.repeat(5200)
        assertEquals(890.0, h.target, 0.0)
    }
}
