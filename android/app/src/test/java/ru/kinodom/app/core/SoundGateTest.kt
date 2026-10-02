package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// План 16В: звуки меню — включены по настройке, только известные, один и тот же не чаще раза в 45 мс
// (зажатая стрелка не трещит).
class SoundGateTest {
    @Test
    fun throttlesSameSound() {
        val g = SoundGate(setOf("move", "select", "back", "edge"))
        assertEquals(true, g.allow("move", on = true, nowMs = 1000))
        assertEquals(false, g.allow("move", on = true, nowMs = 1030))
        assertEquals(true, g.allow("select", on = true, nowMs = 1030))
        assertEquals(true, g.allow("move", on = true, nowMs = 1046))
    }

    @Test
    fun offAndUnknownAreSilent() {
        val g = SoundGate(setOf("move"))
        assertEquals(false, g.allow("move", on = false, nowMs = 1000))
        assertEquals(false, g.allow("beep", on = true, nowMs = 2000))
        assertEquals(true, g.allow("move", on = true, nowMs = 3000))
    }
}
