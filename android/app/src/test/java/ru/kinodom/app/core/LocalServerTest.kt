package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// Сервер на устройстве (полный порт, план 2026-10-06): командная строка, окружение и порты.
class LocalServerTest {
    @Test
    fun commandAndEnv() {
        assertEquals(listOf("/data/app/libkinodomserver.so", "run"), LocalServer.command("/data/app/libkinodomserver.so"))
        val env = LocalServer.env("/data/ru.kinodom.home/files/kinodom")
        assertEquals("/data/ru.kinodom.home/files/kinodom", env["KINODOM_HOME"])
        assertTrue((env["GOMEMLIMIT"] ?: "").endsWith("Mi")) // предел кучи — в мебибайтах
    }

    @Test
    fun portsAndBootstrap() {
        assertTrue(LocalServer.API_PORT != 8090) // не как у сервера на ПК
        assertTrue(LocalServer.TORRENT_PORT != 42090)
        assertEquals("""{"apiPort":${LocalServer.API_PORT},"torrentPort":${LocalServer.TORRENT_PORT}}""", LocalServer.bootstrap())
        assertEquals("http://127.0.0.1:${LocalServer.API_PORT}", LocalServer.baseUrl())
    }
}
