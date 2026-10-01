package ru.kinodom.app.net

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.FileProvider
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import ru.kinodom.app.core.ApkCheck
import ru.kinodom.app.core.ServerApp

// Updater — обновление приложения с сервера Kinodom (спека этапа 13, раздел 6): сведения, загрузка во внутреннюю
// папку, установщик Android.
class Updater(private val context: Context, private val base: String) {
    suspend fun check(): ServerApp? = withContext(Dispatchers.IO) {
        try {
            val c = URL(base + "api/v1/app").openConnection() as HttpURLConnection
            c.connectTimeout = 3000
            c.readTimeout = 3000
            try {
                if (c.responseCode == 200) ServerApp.parse(c.inputStream.bufferedReader().readText()) else null
            } finally {
                c.disconnect()
            }
        } catch (e: Exception) {
            null
        }
    }

    // download — APK в cacheDir/update; progress(0..100); файл не целый (оборвалось, не ZIP) — null.
    suspend fun download(app: ServerApp, progress: (Int) -> Unit): File? = withContext(Dispatchers.IO) {
        val dir = File(context.cacheDir, "update").apply { mkdirs() }
        val f = File(dir, "kinodom.apk")
        f.delete()
        try {
            val c = URL(base + app.url.removePrefix("/")).openConnection() as HttpURLConnection
            c.connectTimeout = 5000
            c.readTimeout = 30000
            try {
                if (c.responseCode != 200) return@withContext null
                var got = 0L
                val head = IntArray(2) { -1 } // первые два байта: ZIP начинается с «PK»
                c.inputStream.use { input ->
                    f.outputStream().use { out ->
                        val buf = ByteArray(64 * 1024)
                        var last = -1
                        while (true) {
                            val n = input.read(buf)
                            if (n < 0) break
                            for (i in 0 until n) {
                                val at = got + i
                                if (at >= 2) break
                                head[at.toInt()] = buf[i].toInt()
                            }
                            out.write(buf, 0, n)
                            got += n
                            val pct = if (app.size > 0) (got * 100 / app.size).toInt().coerceIn(0, 100) else 0
                            if (pct != last) {
                                last = pct
                                withContext(Dispatchers.Main) { progress(pct) }
                            }
                        }
                    }
                }
                val zip = head[0] == 'P'.code && head[1] == 'K'.code
                if (ApkCheck.ok(got, app.size, zip)) f else null
            } finally {
                c.disconnect()
            }
        } catch (e: Exception) {
            null
        }
    }

    // canInstall — Android 8+ спрашивает разрешение «устанавливать из Kinodom» один раз.
    fun canInstall(): Boolean = Build.VERSION.SDK_INT < 26 || context.packageManager.canRequestPackageInstalls()

    fun askInstallPermission(): Intent =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + context.packageName))

    fun installIntent(file: File): Intent {
        val uri = FileProvider.getUriForFile(context, context.packageName + ".files", file)
        return Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
    }
}
