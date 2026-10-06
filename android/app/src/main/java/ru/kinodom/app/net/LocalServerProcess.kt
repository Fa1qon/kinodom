package ru.kinodom.app.net

import android.content.Context
import android.util.Log
import ru.kinodom.app.core.LocalServer
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

// LocalServerProcess — процесс сервера на устройстве (план 2026-10-06): один на приложение.
// Переживает экраны; если приложение убили, процесс уходит вместе с ним (ребёнок), а при
// следующем запуске start переиспользует живой — сначала спрашивается /api/v1/status.
object LocalServerProcess {

    private const val TAG = "KinodomLocal"
    private const val READY_MS = 20_000L

    @Volatile
    private var proc: Process? = null

    fun running(): Boolean = proc?.isAlive == true

    // start — сервер работает и отвечает; иначе запускает и ждёт готовности. false — не вышло.
    fun start(ctx: Context): Boolean {
        if (statusOk()) return true // живой с прошлого раза
        if (proc?.isAlive == true) { // запущен, ещё не отвечает — подождать
            return waitReady()
        }
        val bin = LocalServer.binary(ctx) ?: return false
        val home = LocalServer.ensureHome(ctx)
        return try {
            val pb = ProcessBuilder(*LocalServer.command(bin.absolutePath).toTypedArray())
                .directory(home)
                .redirectErrorStream(false)
            LocalServer.env(home.absolutePath).forEach { (k, v) -> pb.environment()[k] = v }
            proc = pb.start()
            Log.i(TAG, "сервер запущен: ${bin.name}, порт ${LocalServer.API_PORT}")
            waitReady()
        } catch (e: Exception) {
            Log.w(TAG, "сервер не запустился", e)
            false
        }
    }

    private fun waitReady(): Boolean {
        val deadline = System.currentTimeMillis() + READY_MS
        while (System.currentTimeMillis() < deadline) {
            if (statusOk()) return true
            if (proc?.isAlive == false) return false
            Thread.sleep(400)
        }
        return statusOk()
    }

    // statusOk — блокирующий вопрос «жив ли сервер»: из фонового потока запуска.
    private fun statusOk(): Boolean = try {
        val c = URL(LocalServer.baseUrl() + "api/v1/status").openConnection() as HttpURLConnection
        c.connectTimeout = 1500
        c.readTimeout = 1500
        try {
            c.responseCode == 200
        } finally {
            c.disconnect()
        }
    } catch (e: Exception) {
        false
    }
}
