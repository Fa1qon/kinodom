package ru.kinodom.app.core

import org.json.JSONObject

// ServerApp — приложение на сервере (GET /api/v1/app, спека этапа 13, раздел 5.3).
data class ServerApp(val version: String, val versionCode: Int, val url: String, val size: Long) {
    companion object {
        fun parse(json: String): ServerApp? = try {
            val o = JSONObject(json)
            val code = o.optInt("versionCode", 0)
            val url = o.optString("url", "")
            if (code <= 0 || url.isEmpty()) null else ServerApp(o.optString("version", ""), code, url, o.optLong("size", 0))
        } catch (e: Exception) {
            null
        }
    }
}

// UpdateDecision — предлагать ли обновление (спека этапа 13, раздел 6): на сервере новее, а «Позже» не нажимали.
object UpdateDecision {
    fun offer(own: Int, server: ServerApp?, postponed: Boolean): Boolean = !postponed && server != null && server.versionCode > own
}

// ApkCheck — скачанный APK целый: размер сошёлся и это ZIP («PK») — иначе установщик Android не открывается.
object ApkCheck {
    fun ok(downloaded: Long, expected: Long, startsWithZip: Boolean): Boolean = expected > 0 && downloaded == expected && startsWithZip
}
