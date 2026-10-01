package ru.kinodom.app.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// Пульту нужен WebView не старше Chrome 80 (`?.`) — иначе экран «Обновите Android System WebView» (спека 3.3).
class WebViewVersionTest {
    private fun ua(chrome: String) = "Mozilla/5.0 (Linux; Android 7.1.2; TV Box Build/NHG47L; wv) AppleWebKit/537.36 " +
        "(KHTML, like Gecko) Version/4.0 Chrome/$chrome Mobile Safari/537.36"

    @Test
    fun modern() = assertTrue(WebViewVersion.ok(ua("143.0.7499.24")))

    @Test
    fun exactly80() = assertTrue(WebViewVersion.ok(ua("80.0.3987.99")))

    @Test
    fun old() = assertFalse(WebViewVersion.ok(ua("52.0.2743.100")))

    @Test
    fun noChrome() = assertFalse(WebViewVersion.ok("Mozilla/5.0 (Linux; Android 7.0) AppleWebKit/534.30 Version/4.0 Mobile Safari/534.30"))
}
