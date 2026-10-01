package ru.kinodom.app.core

import java.util.TimeZone
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Программа (спека этапа 13, 4.4; план 13b, Review Focus 4): «сейчас» и «дальше», полночь, последняя передача
// дня, время — по поясу каналов из ответа сервера, а не по часам устройства.
class GuideTest {
    private val json = """{"items":[
        {"start":"2026-10-01T22:30:00+07:00","stop":"2026-10-02T00:15:00+07:00","title":"Через полночь"},
        {"start":"2026-10-01T21:00:00+07:00","stop":"2026-10-01T22:30:00+07:00","title":"Вечер"},
        {"start":"2026-10-01T05:00:00Z","stop":"2026-10-01T06:00:00Z","title":"Днём"}],"utcOffset":7}"""

    private fun t(s: String) = Rfc3339.parse(s)!!

    @Test
    fun parsesTimes() {
        assertEquals(t("2026-10-01T14:00:00Z"), t("2026-10-01T21:00:00+07:00"))
        assertEquals(t("2026-10-01T14:00:00Z") + 500, t("2026-10-01T14:00:00.5Z"))
        assertNull(Rfc3339.parse("вчера"))
    }

    @Test
    fun nowAndNext() {
        val e = Epg.parse(json)!!
        assertEquals(7, e.utcOffset)
        val at = t("2026-10-01T21:10:00+07:00")
        assertEquals("Вечер", e.now(at)!!.title)
        assertEquals("Через полночь", e.next(at)!!.title)
        assertFalse(Guide.needTomorrow(e, at))
    }

    @Test
    fun lastOfTheDayNeedsTomorrow() {
        val e = Epg.parse(json)!!
        val at = t("2026-10-01T23:50:00+07:00")
        assertEquals("Через полночь", e.now(at)!!.title)
        assertNull(e.next(at))
        assertTrue(Guide.needTomorrow(e, at))
        val tomorrow = Epg.parse("""{"items":[{"start":"2026-10-02T00:15:00+07:00","stop":"2026-10-02T01:00:00+07:00","title":"Ночь"}],"utcOffset":7}""")!!
        assertEquals("Ночь", (e + tomorrow).next(at)!!.title)
    }

    @Test
    fun gapHasNoNowButNext() {
        val e = Epg.parse(json)!!
        val at = t("2026-10-01T15:00:00+07:00")
        assertNull(e.now(at))
        assertEquals("Вечер", e.next(at)!!.title)
    }

    @Test
    fun empty() {
        val e = Epg.parse("""{"items":[],"utcOffset":3}""")!!
        assertNull(e.now(0))
        assertNull(e.next(0))
        assertFalse(Guide.needTomorrow(e, 0)) // программы нет вовсе — не спрашивать
        assertNull(Epg.parse("не JSON"))
    }

    @Test
    fun zoneOfChannelsNotDevice() {
        val was = TimeZone.getDefault()
        try {
            TimeZone.setDefault(TimeZone.getTimeZone("Europe/Moscow"))
            assertEquals("21:00", Guide.hhmm(t("2026-10-01T14:00:00Z"), 7))
            assertEquals("17:00", Guide.hhmm(t("2026-10-01T14:00:00Z"), 3))
            assertEquals("2026-10-02", Guide.date(1, 7, t("2026-09-30T17:30:00Z"))) // в UTC+7 уже 1 октября
            assertEquals("2026-10-01", Guide.date(1, 3, t("2026-09-30T17:30:00Z")))
        } finally {
            TimeZone.setDefault(was)
        }
    }

    @Test
    fun progress() {
        val p = Prog(t("2026-10-01T21:00:00+07:00"), t("2026-10-01T22:00:00+07:00"), "Час")
        assertEquals(25, Guide.progress(p, t("2026-10-01T21:15:00+07:00")))
        assertEquals(0, Guide.progress(p, t("2026-10-01T20:00:00+07:00")))
        assertEquals(100, Guide.progress(p, t("2026-10-01T23:00:00+07:00")))
        assertEquals(0, Guide.progress(Prog(10, 10, "пусто"), 10))
    }
}
