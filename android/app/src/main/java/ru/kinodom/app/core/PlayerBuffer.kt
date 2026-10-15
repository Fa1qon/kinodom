package ru.kinodom.app.core

/** Буфер VOD-плеера: быстрый старт, но большой запас для нестабильного Wi‑Fi и торрента. */
data class PlayerBuffer(
    val minBufferMs: Int,
    val maxBufferMs: Int,
    val bufferForPlaybackMs: Int,
    val bufferForPlaybackAfterRebufferMs: Int,
)

object PlayerBufferPolicy {
    fun vod() = PlayerBuffer(
        minBufferMs = 60_000,
        maxBufferMs = 300_000,
        bufferForPlaybackMs = 10_000,
        bufferForPlaybackAfterRebufferMs = 20_000,
    )
}
