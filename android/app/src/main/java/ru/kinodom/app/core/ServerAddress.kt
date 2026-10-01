package ru.kinodom.app.core

// Parsed — итог разбора адреса: Ok — адрес пульта «http://хост:порт/», Bad — «Неверный адрес».
sealed interface Parsed {
    data class Ok(val base: String) : Parsed
    data object Bad : Parsed
}

// ServerAddress — адрес Kinodom, введённый человеком (спека этапа 13, раздел 3.2): «192.168.0.10»,
// «192.168.0.10:8097», «http://192.168.0.10:8090/#/…», имя хоста. Сервер — только http: https и другие
// схемы — неверный адрес. Без порта — порт пульта по умолчанию.
object ServerAddress {
    const val DEFAULT_PORT = 8090

    private val hostChars = Regex("^[A-Za-z0-9.-]+$")
    private val digitsAndDots = Regex("^[0-9.]+$")

    fun parse(input: String): Parsed {
        var s = input.trim()
        if (s.isEmpty()) return Parsed.Bad
        val scheme = s.indexOf("://")
        if (scheme >= 0) {
            if (!s.substring(0, scheme).equals("http", ignoreCase = true)) return Parsed.Bad
            s = s.substring(scheme + 3)
        }
        s = s.substringBefore('/').substringBefore('#').substringBefore('?')
        val host: String
        val port: Int
        val colon = s.lastIndexOf(':')
        if (colon >= 0) {
            host = s.substring(0, colon)
            port = s.substring(colon + 1).toIntOrNull() ?: return Parsed.Bad
        } else {
            host = s
            port = DEFAULT_PORT
        }
        if (port !in 1..65535 || !validHost(host)) return Parsed.Bad
        return Parsed.Ok("http://${host.lowercase()}:$port/")
    }

    // validHost — имя из букв, цифр, точек и дефисов; одни цифры и точки — это IPv4, и тогда в нём четыре
    // числа от 0 до 255.
    private fun validHost(host: String): Boolean {
        if (host.isEmpty() || !hostChars.matches(host) || host.startsWith('.') || host.endsWith('.')) return false
        if (!digitsAndDots.matches(host)) return true
        val parts = host.split('.')
        return parts.size == 4 && parts.all { p -> p.isNotEmpty() && p.length <= 3 && p.toInt() in 0..255 }
    }
}
