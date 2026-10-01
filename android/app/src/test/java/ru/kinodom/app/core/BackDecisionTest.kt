package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// «Назад» (план 13a, Review Focus 5): открытое поверх пульта — закрыть; есть куда назад в пульте — назад;
// первый экран пульта — свернуть приложение, а не показать пустой WebView.
class BackDecisionTest {
    @Test
    fun overlayFirst() = assertEquals(Action.CloseOverlay, BackDecision.onBack(canGoBack = true, overlay = true))

    @Test
    fun historyBack() = assertEquals(Action.GoBack, BackDecision.onBack(canGoBack = true, overlay = false))

    @Test
    fun firstScreenMinimizes() = assertEquals(Action.Minimize, BackDecision.onBack(canGoBack = false, overlay = false))
}
