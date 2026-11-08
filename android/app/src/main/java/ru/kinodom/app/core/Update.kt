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

// UpdateDecision — предлагать ли обновление (спека этапа 13, раздел 6): на сервере новее, «Позже» не нажимали
// и эту же сборку не откладывали раньше — отложенную больше не предлагать, только следующую (жалоба 2026-11-08:
// «постоянно просит обновить»).
object UpdateDecision {
    fun offer(own: Int, server: ServerApp?, postponed: Boolean, skipped: Int = 0): Boolean =
        !postponed && server != null && server.versionCode > own && server.versionCode != skipped

    // Сервер раздаёт kinodom.apk — это клиентский flavor. Полное приложение со встроенным
    // сервером обновляется отдельным каналом релизов и не должно ставить клиентский APK.
    fun checksServer(flavor: String): Boolean = flavor == "client"
}

// ApkCheck — скачанный APK целый: размер сошёлся и это ZIP («PK») — иначе установщик Android не открывается.
object ApkCheck {
    // Обновление кладётся во внутреннее хранилище до передачи установщику. Даже если сервер
    // ошибся размером, загрузка не должна занять всё свободное место на ТВ.
    const val MAX_BYTES = 256L * 1024 * 1024

    fun validSize(expected: Long): Boolean = expected in 1..MAX_BYTES

    // Проверка до записи очередного блока: при переполнении лишние байты не попадают на диск.
    fun chunkFits(downloaded: Long, chunk: Int, expected: Long): Boolean =
        downloaded >= 0 && chunk >= 0 && validSize(expected) && downloaded <= expected - chunk

    fun ok(downloaded: Long, expected: Long, startsWithZip: Boolean): Boolean = expected > 0 && downloaded == expected && startsWithZip
}
