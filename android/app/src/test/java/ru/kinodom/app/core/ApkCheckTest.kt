package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Скачанный APK ставится, только если он целый (план 13a, Review Focus 4): размер сошёлся и это ZIP («PK»).
class ApkCheckTest {
    @Test
    fun whole() = assertTrue(ApkCheck.ok(downloaded = 41000000, expected = 41000000, startsWithZip = true))

    @Test
    fun broken() = assertFalse(ApkCheck.ok(downloaded = 12000000, expected = 41000000, startsWithZip = true))

    @Test
    fun notZip() = assertFalse(ApkCheck.ok(downloaded = 41000000, expected = 41000000, startsWithZip = false))

    @Test
    fun unknownSize() = assertFalse(ApkCheck.ok(downloaded = 0, expected = 0, startsWithZip = false))
}
