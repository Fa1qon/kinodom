package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// Строки технической панели плееров (план 2026-10-06, Task B).
class TechInfoTest {
    @Test
    fun codecs() {
        assertEquals("H.264", TechInfo.codec("video/avc"))
        assertEquals("H.265", TechInfo.codec("video/hevc"))
        assertEquals("AAC", TechInfo.codec("audio/mp4a-latm"))
        assertEquals("E-AC-3", TechInfo.codec("audio/eac3"))
        assertEquals("TrueHD", TechInfo.codec("audio/true-hd"))
        assertEquals("FLAC", TechInfo.codec("audio/flac")) // неизвестный — сегмент капсом
        assertEquals("—", TechInfo.codec(null))
        assertEquals("—", TechInfo.codec(""))
    }

    @Test
    fun fpsAndResolution() {
        assertEquals("24 FPS", TechInfo.fps(23.976f))
        assertEquals("—", TechInfo.fps(-1f))
        assertEquals("—", TechInfo.fps(0f))
        assertEquals("1920×804", TechInfo.resolution(1920, 804))
        assertEquals("—", TechInfo.resolution(0, 0))
    }

    @Test
    fun panel() {
        assertEquals(
            listOf("Видео: H.264 · 1920×804 · 24 FPS", "Звук: AAC", "Буфер: 12 с", "Пропущено кадров: 7"),
            TechInfo.panel("video/avc", 1920, 804, 23.976f, "audio/mp4a-latm", 12, 7),
        )
        // кадра ещё нет — только звук
        assertEquals(listOf("Звук: AC-3"), TechInfo.panel(null, 0, 0, -1f, "audio/ac3", 0, 0))
        // ничего не известно
        assertEquals(listOf("Нет данных"), TechInfo.panel(null, 0, 0, -1f, null, 0, 0))
        // пропусков и буфера нет — строк этих нет
        assertEquals(
            listOf("Видео: H.265 · 1280×720", "Звук: AAC"),
            TechInfo.panel("video/hevc", 1280, 720, -1f, "audio/mp4a-latm", 0, 0),
        )
    }
}
