package ru.kinodom.app.core

// Recovery — ПК пропал, пока приложение открыто или свёрнуто (финальное ревью 13a; спека этапа 13, 3.2): что
// делать по итогам повторного поиска.
sealed interface Recovery {
    data object Reload : Recovery                   // нашёлся тот же сервер — перезагрузить пульт
    data class SwitchTo(val base: String) : Recovery // ровно один другой — ПК сменил адрес, перейти на него
    data object Stay : Recovery                     // ни одного или несколько — «Kinodom не отвечает»

    companion object {
        fun after(found: List<Found>, current: String): Recovery = when {
            found.any { it.base == current } -> Reload
            found.size == 1 -> SwitchTo(found[0].base)
            else -> Stay
        }
    }
}
