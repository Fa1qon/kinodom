package ru.kinodom.app.core

// Step — экран запуска (спека этапа 13, раздел 3.2).
sealed interface Step {
    data object Search : Step                          // искать Kinodom в сети
    data class CheckSaved(val base: String) : Step     // проверить запомненный адрес (3 с)
    data class Choose(val list: List<Found>) : Step    // нашлось несколько — выбрать
    data object AskAddress : Step                      // не нашлось — ввести адрес
    data class Open(val base: String) : Step           // открыть пульт
}

// StartFlow — порядок запуска: запомненный адрес → поиск → выбор или ввод адреса.
object StartFlow {
    fun first(saved: String?): Step = if (saved != null) Step.CheckSaved(saved) else Step.Search

    fun afterSavedCheck(saved: String, ok: Boolean): Step = if (ok) Step.Open(saved) else Step.Search

    fun afterSearch(found: List<Found>): Step = when (found.size) {
        0 -> Step.AskAddress
        1 -> Step.Open(found[0].base)
        else -> Step.Choose(found)
    }
}
