package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Решение «пора на запасной путь» (план 2026-10-06, Task C): устойчивая деградация — да,
// одиночный провал, перемотка и разогрев декодера — нет; срабатывает один раз.
class DropWatchTest {
    private val w = DropWatch()

    @Test
    fun quietStreamNeverFallsBack() {
        var at = 0L
        w.onStart(at)
        // по 2 кадра раз в 2 с на 24 FPS — до 60 с: фон, а не деградация
        for (i in 0 until 26) {
            at = 8_000L + i * 2_000
            w.onDropped(at, 2)
            assertFalse(w.shouldFallback(at + 100, 24f))
        }
        assertFalse(w.fired())
    }

    @Test
    fun singleSpikeDoesNotFallBack() {
        w.onStart(0)
        w.onDropped(9_000, 30) // провал на секунду
        assertFalse(w.shouldFallback(13_000, 24f))
        assertFalse(w.shouldFallback(20_000, 24f))
        assertFalse(w.shouldFallback(21_000, 24f)) // всплеск уже вышел из окна
        assertFalse(w.fired())
    }

    @Test
    fun sustainedDropsFallBack() {
        w.onStart(0)
        var at = 8_000L
        while (at < 14_000) {
            at += 1_000
            w.onDropped(at, 6) // 6 кадров в секунду на 24 FPS — четверть эфира
        }
        // окно наполнено (всплески с 9-й по 14-ю секунду), доля устойчиво выше порога
        assertTrue(w.shouldFallback(at + 100, 24f))
        assertTrue(w.fired())
    }

    @Test
    fun firesOnce() {
        w.onStart(0)
        var at = 8_000L
        while (at < 14_000) {
            at += 1_000
            w.onDropped(at, 12)
        }
        assertTrue(w.shouldFallback(at + 100, 24f))
        w.onDropped(at + 200, 100)
        assertFalse(w.shouldFallback(at + 300, 24f)) // решение одно: плеер уже ушёл на запасной путь
    }

    @Test
    fun graceAfterStart() {
        w.onStart(0)
        var at = 1_000L
        while (at < 6_000) {
            at += 500
            w.onDropped(at, 24) // декодер разогревается — густо, но это первые секунды
        }
        assertFalse(w.shouldFallback(6_000, 24f)) // грат 8 с ещё не прошёл
        assertTrue(w.shouldFallback(14_000, 24f))
    }

    @Test
    fun unknownFpsUsesAbsolute() {
        w.onStart(0)
        var at = 8_000L
        while (at < 14_000) {
            at += 1_000
            w.onDropped(at, 9) // 9 кадров в секунду без fps — больше порога 8
        }
        assertTrue(w.shouldFallback(at + 100, -1f))
        w.onStart(0)
        at = 8_000L
        while (at < 20_000) {
            at += 2_000
            w.onDropped(at, 2) // 1 кадр в секунду — нет
        }
        assertFalse(w.shouldFallback(at, -1f))
    }

    @Test
    fun restartResetsDecision() {
        w.onStart(0)
        var at = 8_000L
        while (at < 14_000) {
            at += 1_000
            w.onDropped(at, 12)
        }
        assertTrue(w.shouldFallback(at + 100, 24f))
        w.onStart(at + 1_000) // новый источник (запасной путь) — наблюдение заново
        assertFalse(w.fired())
        assertFalse(w.shouldFallback(at + 2_000, 24f))
    }
}
