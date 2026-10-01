package ru.kinodom.app.core

// Plate — строки плашки плеера (спека этапа 13, 4.4): «1 Первый канал · МСК+4», «Сейчас: 21:00–22:30 …»,
// «Дальше: 22:30 …»; время — по поясу каналов.
object Plate {
    fun title(ch: Ch): String {
        val name = if (ch.number > 0) "${ch.number} ${ch.name}" else ch.name
        return if (ch.label.isNotEmpty()) "$name · ${ch.label}" else name
    }

    fun now(p: Prog?, offset: Int): String? =
        p?.let { "Сейчас: ${Guide.hhmm(it.start, offset)}–${Guide.hhmm(it.stop, offset)} ${it.title}" }

    fun next(p: Prog?, offset: Int): String? = p?.let { "Дальше: ${Guide.hhmm(it.start, offset)} ${it.title}" }
}
