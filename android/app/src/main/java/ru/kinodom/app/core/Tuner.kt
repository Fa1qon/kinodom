package ru.kinodom.app.core

// Tuner — листание и набор номера в плеере (спека этапа 13, 4.3). Время — снаружи: экран применяет
// набранное через ZAP_MS без нажатий (commit) и номер — через NUMBER_MS после последней цифры (commitNumber).
class Tuner(private val items: List<Ch>, current: Int) {
    var current = current
        private set
    private var pending: Int? = null
    private var digits = ""

    // step — соседний канал (±1 к набранному, по кругу); сбрасывает набор номера. Возвращает индекс для плашки.
    fun step(d: Int): Int {
        digits = ""
        val from = pending ?: current
        val n = items.size
        val to = ((from + d) % n + n) % n
        pending = to
        return to
    }

    // commit — применить листание: новый текущий.
    fun commit(): Int {
        pending?.let { current = it }
        pending = null
        return current
    }

    // jump — включить канал из списка поверх видео сразу.
    fun jump(i: Int) {
        pending = null
        digits = ""
        if (i in items.indices) current = i
    }

    // digit — цифра номера (до четырёх); отменяет листание. Возвращает набранное.
    fun digit(n: Int): String {
        pending = null
        if (digits.length < 4) digits += n
        return digits
    }

    // commitNumber — канал с набранным номером; нет (или ничего не набрано) — null.
    fun commitNumber(): Int? {
        val number = digits.toIntOrNull()
        digits = ""
        val i = if (number == null) -1 else items.indexOfFirst { it.number == number }
        if (i < 0) return null
        current = i
        return i
    }

    companion object {
        const val ZAP_MS = 700L
        const val NUMBER_MS = 1500L
    }
}
