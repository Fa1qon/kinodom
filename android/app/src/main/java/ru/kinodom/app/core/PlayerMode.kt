package ru.kinodom.app.core

// PlayerMode — плеер каналов в настройках приложения (спека этапа 13, раздел 2 «Системный плеер»): встроенный
// (по умолчанию) или системный — VLC и другие плееры Android, как в 13a.
enum class PlayerMode(val id: String) {
    Builtin("builtin"),
    System("system");

    companion object {
        fun of(id: String?): PlayerMode? = entries.firstOrNull { it.id == id }
    }
}
