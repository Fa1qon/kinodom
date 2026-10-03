package ru.kinodom.app.core

// SeekSteps — шаг перемотки с пульта ТВ по времени удержания (спека 18, 6.3).
object SeekSteps {
    fun step(heldMs: Long): Int = when {
        heldMs < 2000 -> 10
        heldMs < 5000 -> 30
        else -> 60
    }

    // REPEAT_MS — шаги при удержании не чаще: повторы клавиши у пульта — каждые 50 мс.
    const val REPEAT_MS = 300L
}

// Hold — удержание ←/→: первое нажатие — шаг 10 с, повторы — шаг по времени удержания; перемотка — по отпусканию, к target.
class Hold(private val dir: Int, base: Double, private val start: Long) {
    var target: Double = base + dir * SeekSteps.step(0)
        private set
    private var last = start

    // repeat — повтор клавиши в момент now: сделал ли он шаг.
    fun repeat(now: Long): Boolean {
        if (now - last < SeekSteps.REPEAT_MS) return false
        last = now
        target += dir * SeekSteps.step(now - start)
        return true
    }
}
