package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// Листание и номер (спека этапа 13, 4.3; план 13b, Review Focus 3): быстрые нажатия включают один канал —
// на котором остановились; цифры — номер канала; номера нет — null («Нет канала N»).
class TunerTest {
    private fun ch(key: String, number: Int = 0) = Ch(key, key, key, number, "", "")
    private val five = listOf(ch("a", 1), ch("b", 2), ch("c", 12), ch("d"), ch("e", 4))

    @Test
    fun tenStepsOneCommit() {
        val t = Tuner(five, 3)
        var shown = -1
        repeat(10) { shown = t.step(1) }
        assertEquals(3, shown)
        assertEquals(3, t.current) // ещё не применено
        assertEquals(3, t.commit())
    }

    @Test
    fun wraps() {
        val t = Tuner(five, 0)
        assertEquals(4, t.step(-1))
        assertEquals(4, t.commit())
        assertEquals(0, t.step(1))
        assertEquals(0, t.commit())
    }

    @Test
    fun commitWithoutStepsKeepsCurrent() = assertEquals(2, Tuner(five, 2).commit())

    @Test
    fun number() {
        val t = Tuner(five, 0)
        assertEquals("1", t.digit(1))
        assertEquals("12", t.digit(2))
        assertEquals(2, t.commitNumber())
        assertEquals(2, t.current)
    }

    @Test
    fun noSuchNumber() {
        val t = Tuner(five, 1)
        t.digit(7)
        assertNull(t.commitNumber())
        assertEquals(1, t.current)
    }

    @Test
    fun fourDigitsMax() {
        val t = Tuner(five, 0)
        listOf(1, 2, 3, 4).forEach { t.digit(it) }
        assertEquals("1234", t.digit(5))
    }

    @Test
    fun stepAfterDigitsDropsThem() {
        val t = Tuner(five, 0)
        t.digit(1)
        t.digit(2)
        assertEquals(1, t.step(1))
        assertNull(t.commitNumber()) // набор сброшен
        assertEquals(1, t.commit())
    }

    @Test
    fun digitAfterStepDropsStep() {
        val t = Tuner(five, 0)
        t.step(1)
        t.digit(4)
        assertEquals(0, t.commit()) // листание отменено набором
        assertEquals(4, t.commitNumber())
    }

    @Test
    fun jump() {
        val t = Tuner(five, 0)
        t.step(1)
        t.jump(3)
        assertEquals(3, t.current)
        assertEquals(3, t.commit())
    }
}
