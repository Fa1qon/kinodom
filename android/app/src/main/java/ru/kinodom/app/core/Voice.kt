package ru.kinodom.app.core

// Voice — голосовой поиск (план 17В): что из распознанного искать и как передать это пульту.
object Voice {
    // query — первая непустая строка распознавания (они по убыванию уверенности); нет — null.
    fun query(results: List<String>?): String? = results?.map { it.trim() }?.firstOrNull { it.isNotEmpty() }

    // hashJs — JS для WebView: страница поиска с текстом. Текст — строкой JSON: кавычки, обратная косая, переводы
    // строк и «<» экранируются, чтобы распознанное не стало кодом.
    fun hashJs(text: String): String = "location.hash='#/search?q='+encodeURIComponent(${jsonString(text)})"

    private fun jsonString(s: String): String {
        val b = StringBuilder("\"")
        for (c in s) {
            when {
                c == '"' -> b.append("\\\"")
                c == '\\' -> b.append("\\\\")
                c == '\n' -> b.append("\\n")
                c == '\r' -> b.append("\\r")
                c == '\t' -> b.append("\\t")
                c == '<' || c < ' ' || c == '\u2028' || c == '\u2029' -> b.append(String.format("\\u%04x", c.code))
                else -> b.append(c)
            }
        }
        return b.append('"').toString()
    }
}
