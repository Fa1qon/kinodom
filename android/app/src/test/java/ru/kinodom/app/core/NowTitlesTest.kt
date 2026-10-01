package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// «Что идёт сейчас» в списке поверх видео (спека этапа 13, 4.3) — из GET /api/v1/channels: по ключу версии и
// по ключу канала (выбранная на странице версия — не та, что в общем списке).
class NowTitlesTest {
    @Test
    fun parses() {
        val json = """{"channels":[
            {"key":"pervy","version":"pervy-pl4","now":{"start":"2026-10-01T21:00:00+07:00","stop":"2026-10-01T22:00:00+07:00","title":"Большая игра"}},
            {"key":"ntv","version":"ntv","now":null},{"key":"kino","version":"kino"}],"utcOffset":7}"""
        val m = NowTitles.parse(json)
        assertEquals("Большая игра", m["pervy-pl4"])
        assertEquals("Большая игра", m["pervy"])
        assertEquals(null, m["ntv"])
        assertEquals(2, m.size)
    }

    @Test
    fun titleFor() {
        val m = mapOf("pervy-pl4" to "Большая игра", "pervy" to "Большая игра (МСК+4)")
        assertEquals("Большая игра", NowTitles.titleFor(m, Ch("pervy", "pervy-pl4", "Первый", 1, "", "")))
        assertEquals("Большая игра (МСК+4)", NowTitles.titleFor(m, Ch("pervy", "pervy-mn1", "Первый", 1, "", "")))
        assertEquals("", NowTitles.titleFor(m, Ch("ntv", "ntv", "НТВ", 4, "", "")))
    }

    @Test
    fun garbage() = assertEquals(emptyMap<String, String>(), NowTitles.parse("не JSON"))
}
