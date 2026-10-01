package ru.kinodom.app.core

// Action — что сделать по «Назад».
enum class Action { CloseOverlay, GoBack, Minimize }

// BackDecision — «Назад» пульта ТВ и телефона в пульте (спека этапа 13, раздел 3.3): открытое поверх пульта —
// закрыть; есть куда назад — назад по истории пульта; первый экран — свернуть приложение, как другие ТВ-приложения.
object BackDecision {
    fun onBack(canGoBack: Boolean, overlay: Boolean): Action = when {
        overlay -> Action.CloseOverlay
        canGoBack -> Action.GoBack
        else -> Action.Minimize
    }
}
