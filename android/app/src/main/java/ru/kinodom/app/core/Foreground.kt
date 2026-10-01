package ru.kinodom.app.core

// Foreground — сколько экранов приложения сейчас видно (пульт, плеер): переход с нуля — вход в приложение (запуск
// или возврат из фона). При входе пульт спрашивает сервер про новую версию (заказчик 2026-10-01).
class Foreground {
    private var started = 0

    // start — экран появился; true — это вход.
    fun start(): Boolean {
        started++
        return started == 1
    }

    fun stop() {
        if (started > 0) started--
    }

    companion object {
        val app = Foreground() // на весь процесс; экраны зовут с главного потока
    }
}
