package ru.kinodom.app.core

// Failover — перебор источников канала (спека этапа 13, 4.2): играет первый; ошибка, 8 с без картинки, конец
// живого потока — следующий; кончились — null («Канал сейчас не показывает»).
class Failover(private val size: Int) {
    var index = 0
        private set

    fun onFailed(): Int? {
        if (index + 1 >= size) return null
        index++
        return index
    }

    companion object {
        const val SILENCE_MS = 8000L

        // accepts — источник работает: показал кадр, или у него только звук (радио) и звук есть.
        fun accepts(video: Boolean, audio: Boolean, firstFrame: Boolean): Boolean = firstFrame || (!video && audio)
    }
}
