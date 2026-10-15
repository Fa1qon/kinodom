package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Скачанный APK ставится, только если он целый (план 13a, Review Focus 4): размер сошёлся и это ZIP («PK»).
class ApkCheckTest {
    @Test
    fun sizeIsBounded() {
        assertTrue(ApkCheck.validSize(1))
        assertTrue(ApkCheck.validSize(ApkCheck.MAX_BYTES))
        assertFalse(ApkCheck.validSize(0))
        assertFalse(ApkCheck.validSize(ApkCheck.MAX_BYTES + 1))
        assertFalse(ApkCheck.validSize(Long.MAX_VALUE))
    }

    @Test
    fun oversizedChunkIsRejected() {
        assertTrue(ApkCheck.chunkFits(100, 20, 200))
        assertFalse(ApkCheck.chunkFits(190, 20, 200))
        assertFalse(ApkCheck.chunkFits(0, 1, 0))
    }

    @Test
    fun whole() = assertTrue(ApkCheck.ok(downloaded = 41000000, expected = 41000000, startsWithZip = true))

    @Test
    fun broken() = assertFalse(ApkCheck.ok(downloaded = 12000000, expected = 41000000, startsWithZip = true))

    @Test
    fun notZip() = assertFalse(ApkCheck.ok(downloaded = 41000000, expected = 41000000, startsWithZip = false))

    @Test
    fun unknownSize() = assertFalse(ApkCheck.ok(downloaded = 0, expected = 0, startsWithZip = false))
}
