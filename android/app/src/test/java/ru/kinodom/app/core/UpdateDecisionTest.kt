package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Обновление с сервера (спека этапа 13, раздел 6; план 13a, Review Focus 4): новее и не отложено — предложить;
// та же, старше, нет сведений, «Позже» — нет.
class UpdateDecisionTest {
    private fun app(code: Int) = ServerApp("0.11.0-x", code, "/app/kinodom.apk", 1000)

    @Test
    fun newerOffered() = assertTrue(UpdateDecision.offer(own = 5, server = app(6), postponed = false))

    @Test
    fun sameNotOffered() = assertFalse(UpdateDecision.offer(own = 5, server = app(5), postponed = false))

    @Test
    fun olderNotOffered() = assertFalse(UpdateDecision.offer(own = 5, server = app(4), postponed = false))

    @Test
    fun noInfoNotOffered() = assertFalse(UpdateDecision.offer(own = 5, server = null, postponed = false))

    @Test
    fun postponedNotOffered() = assertFalse(UpdateDecision.offer(own = 5, server = app(6), postponed = true))

    @Test
    fun parseServerApp() = assertEquals(
        ServerApp("0.11.0-abc1234", 512, "/app/kinodom.apk", 41000000),
        ServerApp.parse("""{"version":"0.11.0-abc1234","versionCode":512,"size":41000000,"url":"/app/kinodom.apk"}"""),
    )

    @Test
    fun parseWithoutCode() = assertNull(ServerApp.parse("""{"version":"0.11.0","url":"/app/kinodom.apk","size":1}"""))

    @Test
    fun parseGarbage() = assertNull(ServerApp.parse("<html>404</html>"))
}
