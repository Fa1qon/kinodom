package ru.kinodom.app.net

import java.net.HttpURLConnection
import java.net.URL
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

// Api — GET JSON с сервера Kinodom для плеера каналов (источники, программа, список): base — адрес пульта
// («http://192.168.0.10:8090/»), path — после /api/v1/. Ошибка сети, не 200 — null.
object Api {
    suspend fun get(base: String, path: String, timeoutMs: Int = 8000): String? = withContext(Dispatchers.IO) {
        try {
            val c = URL(base + "api/v1/" + path.removePrefix("/")).openConnection() as HttpURLConnection
            c.connectTimeout = timeoutMs
            c.readTimeout = timeoutMs
            try {
                if (c.responseCode == 200) c.inputStream.bufferedReader().readText() else null
            } finally {
                c.disconnect()
            }
        } catch (e: Exception) {
            null
        }
    }

    // put — PUT JSON (место просмотра, план 18В); удалось — true.
    suspend fun put(base: String, path: String, json: String, timeoutMs: Int = 8000): Boolean = withContext(Dispatchers.IO) {
        try {
            val c = URL(base + "api/v1/" + path.removePrefix("/")).openConnection() as HttpURLConnection
            c.connectTimeout = timeoutMs
            c.readTimeout = timeoutMs
            c.requestMethod = "PUT"
            c.doOutput = true
            c.setRequestProperty("Content-Type", "application/json")
            try {
                c.outputStream.use { it.write(json.toByteArray()) }
                c.responseCode in 200..299
            } finally {
                c.disconnect()
            }
        } catch (e: Exception) {
            false
        }
    }
}
