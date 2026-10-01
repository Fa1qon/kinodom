package ru.kinodom.app.net

import java.net.HttpURLConnection
import java.net.URL
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

// Status — отвечает ли Kinodom по адресу пульта: GET /api/v1/status за 3 с (спека этапа 13, раздел 3.2).
object Status {
    suspend fun ok(base: String, timeoutMs: Int = 3000): Boolean = withContext(Dispatchers.IO) {
        try {
            val c = URL(base + "api/v1/status").openConnection() as HttpURLConnection
            c.connectTimeout = timeoutMs
            c.readTimeout = timeoutMs
            try {
                c.responseCode == 200
            } finally {
                c.disconnect()
            }
        } catch (e: Exception) {
            false
        }
    }
}
