package ru.kinodom.app.core

// BufferWatch — обнаружение повторной буферизации прямого источника. Некоторые ТВ не сообщают
// пропущенные кадры, но каждые несколько секунд снова входят в BUFFERING и откатывают позицию.
// После устойчивого повторения плеер один раз переходит на поток сервера.
class BufferWatch(
    private val graceMs: Long = 8_000,
    private val windowMs: Long = 30_000,
    private val limit: Int = 3,
) {
    private var startedAt = -1L
    private val events = ArrayDeque<Long>()
    private var fired = false

    fun onStart(at: Long) {
        startedAt = at
        events.clear()
        fired = false
    }

    fun onBuffer(at: Long) {
        if (!fired && startedAt >= 0) events.addLast(at)
    }

    fun shouldFallback(at: Long): Boolean {
        if (fired || startedAt < 0 || at - startedAt < graceMs) return false
        while (events.isNotEmpty() && at - events.first() > windowMs) events.removeFirst()
        if (events.size < limit) return false
        fired = true
        return true
    }

    fun fired(): Boolean = fired
}
