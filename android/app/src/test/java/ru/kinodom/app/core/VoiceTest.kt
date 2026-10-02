package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// План 17В: голосовой поиск — первая непустая строка распознавания; переход пульта — JS с текстом в кавычках JSON.
class VoiceTest {
    @Test
    fun picksFirstNonBlank() {
        assertEquals("игра престолов", Voice.query(listOf("  ", " игра престолов ", "игра")))
        assertNull(Voice.query(null))
        assertNull(Voice.query(listOf(" ")))
    }

    @Test
    fun hashJsEscapes() {
        // Ожидаемое — сырой строкой: кавычки, обратная косая, перевод строки и «<» экранированы.
        val want = """location.hash='#/search?q='+encodeURIComponent("a\"b\\c\n\u003c/script>")"""
        assertEquals(want, Voice.hashJs("a\"b\\c\n</script>"))
    }

    // Ревью 17В: разделители строк U+2028/2029, возврат каретки, табуляция, управляющие и эмодзи — не ломают строку JS.
    @Test
    fun hashJsEscapesSeparators() {
        val js = Voice.hashJs("a\u2028b\u2029c\r\t\u0001😀")
        assertEquals("""location.hash='#/search?q='+encodeURIComponent("a\u2028b\u2029c\r\t\u0001😀")""", js)
    }
}
