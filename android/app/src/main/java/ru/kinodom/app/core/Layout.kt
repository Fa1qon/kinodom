package ru.kinodom.app.core

// Layout — размеры экранов приложения: поле и кнопки — не шире 420 dp и не шире экрана с отступами по 32 dp
// (на телефоне 360–384 dp стоя они обрезались — финальное ревью 13a), но не уже 120 dp.
object Layout {
    const val MAX_FIELD_DP = 420
    const val SIDE_DP = 32

    fun fieldWidthDp(screenDp: Int): Int = (screenDp - 2 * SIDE_DP).coerceIn(120, MAX_FIELD_DP)
}
