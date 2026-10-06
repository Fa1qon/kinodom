package ru.kinodom.app.core

// DropWatch — решение «пора на запасной путь» при прямом воспроизведении AVI/MP4 (план 2026-10-06,
// Task C; спека единого плеера, раздел «AVI/MP4 и FPS»): запасной поток сервера чинит контейнерные
// подтормаживания, поэтому уходит на него плеер только при устойчивой деградации — не из-за одиночного
// провала или перемотки. Скользящее окно всплесков пропущенных кадров; доля считается от fps формата;
// fps неизвестен — абсолютный порог в кадрах в секунду. Срабатывает один раз: новый источник — onStart.
class DropWatch(
    private val graceMs: Long,     // после старта источника декодер разогревается — решений нет
    private val windowMs: Long,    // окно наблюдения: всплески старше — выбрасываются
    private val minWindowMs: Long, // решение не раньше, чем окно наполнено столько
    private val ratio: Double,     // доля пропущенных от ожидаемых кадров (fps известен)
    private val absFps: Double,    // fps неизвестен: столько пропущенных в секунду окна
) {
    constructor() : this(graceMs = 8_000, windowMs = 12_000, minWindowMs = 5_000, ratio = 0.15, absFps = 8.0)

    private var startedAt = -1L
    private val bursts = ArrayDeque<Long>() // моменты всплесков; размер всплеска — параллельный список
    private val sizes = ArrayDeque<Int>()
    private var fired = false

    // onStart — источник открыт заново: счёт и решение с нуля.
    fun onStart(at: Long) {
        startedAt = at
        bursts.clear()
        sizes.clear()
        fired = false
    }

    // onDropped — очередной всплеск пропущенных кадров (AnalyticsListener.onDroppedVideoFrames).
    fun onDropped(at: Long, frames: Int) {
        if (fired || frames <= 0) return
        if (startedAt < 0) startedAt = at
        bursts.addLast(at)
        sizes.addLast(frames)
    }

    // shouldFallback — устойчиво тормозит? fps — из формата видео; 0 и меньше — неизвестен.
    fun shouldFallback(at: Long, fps: Float): Boolean {
        if (fired || startedAt < 0 || at - startedAt < graceMs) return false
        trim(at)
        if (bursts.isEmpty()) return false
        val spanMs = at - bursts.first()
        if (spanMs < minWindowMs) return false
        val perSec = sizes.sum().toDouble() / (spanMs / 1000.0)
        if (if (fps > 0f) perSec < fps * ratio else perSec < absFps) return false
        fired = true
        return true
    }

    fun fired(): Boolean = fired

    private fun trim(at: Long) {
        while (bursts.isNotEmpty() && at - bursts.first() > windowMs) {
            bursts.removeFirst()
            sizes.removeFirst()
        }
    }
}
