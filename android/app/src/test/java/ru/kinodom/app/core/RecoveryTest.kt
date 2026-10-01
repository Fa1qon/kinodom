package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// ПК пропал, пока приложение живо (финальное ревью 13a, Important 3; Review Focus 2 и 5): поиск нашёл тот же
// сервер — перезагрузить пульт; ровно один другой (ПК сменил адрес) — перейти на него; ни одного или несколько —
// остаться на «Kinodom не отвечает».
class RecoveryTest {
    private val current = "http://192.168.0.20:8090/"
    private val moved = Found("http://192.168.0.26:8090/", "Kinodom на PC")

    @Test
    fun sameServerReloads() = assertEquals(Recovery.Reload, Recovery.after(listOf(Found(current, "Kinodom на PC")), current))

    @Test
    fun movedSwitches() = assertEquals(Recovery.SwitchTo(moved.base), Recovery.after(listOf(moved), current))

    @Test
    fun noneStays() = assertEquals(Recovery.Stay, Recovery.after(emptyList(), current))

    @Test
    fun severalStay() = assertEquals(Recovery.Stay, Recovery.after(listOf(moved, Found("http://192.168.0.31:8090/", "Kinodom на LAPTOP")), current))

    @Test
    fun severalWithCurrentReloads() = assertEquals(Recovery.Reload, Recovery.after(listOf(moved, Found(current, "Kinodom на PC")), current))
}
