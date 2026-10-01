package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// Порядок запуска (спека этапа 13, раздел 3.2): сохранённый адрес — проверить; не отвечает — искать (план 13a,
// Review Focus 2: ПК сменил адрес); один — открыть, несколько — выбрать, ни одного — ввести адрес.
class StartFlowTest {
    private val pc = Found("http://192.168.0.26:8090/", "Kinodom на PC")
    private val laptop = Found("http://192.168.0.31:8090/", "Kinodom на LAPTOP")

    @Test
    fun noSavedSearches() = assertEquals(Step.Search, StartFlow.first(null))

    @Test
    fun savedIsCheckedFirst() = assertEquals(Step.CheckSaved("http://192.168.0.20:8090/"), StartFlow.first("http://192.168.0.20:8090/"))

    @Test
    fun savedAnswersOpens() = assertEquals(Step.Open("http://192.168.0.20:8090/"), StartFlow.afterSavedCheck("http://192.168.0.20:8090/", true))

    @Test
    fun savedSilentSearches() = assertEquals(Step.Search, StartFlow.afterSavedCheck("http://192.168.0.20:8090/", false))

    @Test
    fun pcMovedFoundOnNewAddress() {
        val step = StartFlow.afterSavedCheck("http://192.168.0.20:8090/", false)
        assertEquals(Step.Search, step)
        assertEquals(Step.Open(pc.base), StartFlow.afterSearch(listOf(pc)))
    }

    @Test
    fun twoServersChoose() = assertEquals(Step.Choose(listOf(pc, laptop)), StartFlow.afterSearch(listOf(pc, laptop)))

    @Test
    fun noneAsksAddress() = assertEquals(Step.AskAddress, StartFlow.afterSearch(emptyList()))
}
