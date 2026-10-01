package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Плеер каналов в настройках приложения (спека этапа 13, раздел 2 «Системный плеер»).
class PlayerModeTest {
    @Test
    fun known() {
        assertEquals(PlayerMode.Builtin, PlayerMode.of("builtin"))
        assertEquals(PlayerMode.System, PlayerMode.of("system"))
        assertEquals("system", PlayerMode.System.id)
    }

    @Test
    fun unknownIsNull() {
        assertNull(PlayerMode.of("vlc"))
        assertNull(PlayerMode.of(""))
        assertNull(PlayerMode.of(null))
    }
}
