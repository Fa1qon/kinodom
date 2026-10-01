package ru.kinodom.app.core

// WebViewVersion — пульту нужен WebView не старше Chrome 80: он пользуется `?.` (спека этапа 13, раздел 3.3).
object WebViewVersion {
    const val MIN_CHROME = 80
    private val chrome = Regex("""Chrome/(\d+)\.""")

    fun ok(userAgent: String): Boolean {
        val major = chrome.find(userAgent)?.groupValues?.get(1)?.toIntOrNull() ?: return false
        return major >= MIN_CHROME
    }
}
