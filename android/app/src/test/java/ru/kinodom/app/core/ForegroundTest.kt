package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Вход в приложение (заказчик 2026-10-01: проверять обновление на сервере при входе): запуск или возврат из фона —
// вход; пульт → плеер → «Назад» — нет (экран плеера открывается раньше, чем пульт уходит с экрана).
class ForegroundTest {
    @Test
    fun launchIsEntry() = assertTrue(Foreground().start())

    @Test
    fun playerOverPultIsNotEntry() {
        val f = Foreground()
        assertTrue(f.start()) // пульт
        assertFalse(f.start()) // плеер поверх
        f.stop() // пульт ушёл под плеер
        assertFalse(f.start()) // «Назад»: пульт снова на экране
        f.stop() // плеер закрыт
    }

    @Test
    fun backFromBackgroundIsEntry() {
        val f = Foreground()
        f.start()
        f.stop() // «Домой»
        assertTrue(f.start())
    }

    @Test
    fun extraStopIsHarmless() {
        val f = Foreground()
        f.stop()
        assertTrue(f.start())
        assertFalse(f.start())
    }
}
