package ru.kinodom.app.core

// SoundGate — играть ли звук меню (план 16В): только известный, при включённой настройке, один и тот же — не чаще
// раза в GAP_MS (зажатая стрелка не трещит). Время — параметр: проверяется без часов.
class SoundGate(private val names: Set<String>) {
    private val last = HashMap<String, Long>()

    @Synchronized
    fun allow(name: String, on: Boolean, nowMs: Long): Boolean {
        if (!on || name !in names) return false
        val prev = last[name]
        if (prev != null && nowMs - prev < GAP_MS) return false
        last[name] = nowMs
        return true
    }

    companion object {
        const val GAP_MS = 45L
    }
}
