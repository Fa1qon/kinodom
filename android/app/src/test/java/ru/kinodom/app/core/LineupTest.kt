package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Список каналов из моста пульта (спека этапа 13, 5.2; план 13b, Review Focus 5): мусор — null, а не падение.
class LineupTest {
    private val json = """{"list":[{"key":"pervy","version":"pervy-pl4","name":"Первый канал","number":1,"logo":"/logo/pervy-pl4","label":"МСК+4"},
        {"key":"ntv","version":"ntv","name":"НТВ","number":4,"logo":"","label":""},{"key":"kino","name":"Кино"}],"start":1,"listName":"Федеральные"}"""

    @Test
    fun parses() {
        val l = Lineup.parse(json)!!
        assertEquals(3, l.items.size)
        assertEquals(Ch("pervy", "pervy-pl4", "Первый канал", 1, "/logo/pervy-pl4", "МСК+4"), l.items[0])
        assertEquals(1, l.start)
        assertEquals("Федеральные", l.listName)
        assertEquals(Ch("kino", "kino", "Кино", 0, "", ""), l.items[2]) // без version — ключ канала
    }

    @Test
    fun garbageIsNull() {
        assertNull(Lineup.parse("не JSON"))
        assertNull(Lineup.parse("{}"))
        assertNull(Lineup.parse("""{"list":[],"start":0}"""))
        assertNull(Lineup.parse("""{"list":[{"name":"без ключа"}],"start":0}"""))
    }

    @Test
    fun startOutOfRangeIsFirst() {
        assertEquals(0, Lineup.parse("""{"list":[{"key":"a","name":"А"},{"key":"b","name":"Б"}],"start":7}""")!!.start)
        assertEquals(0, Lineup.parse("""{"list":[{"key":"a","name":"А"}],"start":-1}""")!!.start)
    }

    @Test
    fun skippedItemsKeepStart() {
        // Без имени — пропуск; стартовый остаётся тем же каналом.
        val l = Lineup.parse("""{"list":[{"key":"a"},{"key":"b","name":"Б"},{"key":"c","name":"В"}],"start":2,"listName":"Все"}""")!!
        assertEquals(listOf("b", "c"), l.items.map { it.key })
        assertEquals(1, l.start)
    }
}
