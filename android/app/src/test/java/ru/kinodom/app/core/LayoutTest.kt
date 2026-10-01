package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// Поля и кнопки экранов приложения — не шире 420 dp и не шире экрана с отступами по 32 dp (финальное ревью 13a:
// на телефоне 360–384 dp стоя они обрезались с обеих сторон).
class LayoutTest {
    @Test
    fun tvKeeps420() = assertEquals(420, Layout.fieldWidthDp(960))

    @Test
    fun narrowPhoneFits() = assertEquals(296, Layout.fieldWidthDp(360))

    @Test
    fun phone384() = assertEquals(320, Layout.fieldWidthDp(384))

    @Test
    fun tinyNotNegative() = assertEquals(120, Layout.fieldWidthDp(100))
}
