package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Плашка (спека этапа 13, 4.4): номер, имя, подпись версии; «Сейчас» и «Дальше» по поясу каналов.
class PlateTest {
    private fun t(s: String) = Rfc3339.parse(s)!!

    @Test
    fun title() {
        assertEquals("1 Первый канал · МСК+4", Plate.title(Ch("pervy", "pervy-pl4", "Первый канал", 1, "", "МСК+4")))
        assertEquals("Кино", Plate.title(Ch("kino", "kino", "Кино", 0, "", "")))
    }

    @Test
    fun lines() {
        val p = Prog(t("2026-10-01T14:00:00Z"), t("2026-10-01T15:30:00Z"), "Новости")
        assertEquals("Сейчас: 21:00–22:30 Новости", Plate.now(p, 7))
        assertEquals("Дальше: 21:00 Новости", Plate.next(p, 7))
        assertNull(Plate.now(null, 7))
        assertNull(Plate.next(null, 7))
    }
}
