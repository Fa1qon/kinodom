package ru.kinodom.app.net

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import android.widget.ImageView
import java.net.HttpURLConnection
import java.net.URL
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Logos — логотипы каналов с сервера Kinodom (путь из списка, «/logo/<ключ>») для плеера: в памяти до 200 штук,
// загрузка в фоне; картинку, которую Android не читает (SVG), — без логотипа.
class Logos(private val base: String, private val scope: CoroutineScope) {
    private val cache = LruCache<String, Bitmap>(200)
    private val missing = HashSet<String>()

    fun into(v: ImageView, path: String) {
        v.tag = path
        v.setImageDrawable(null)
        if (path.isEmpty() || path in missing) return
        cache.get(path)?.let {
            v.setImageBitmap(it)
            return
        }
        scope.launch {
            val bmp = withContext(Dispatchers.IO) { load(path) }
            if (bmp == null) {
                missing += path
                return@launch
            }
            cache.put(path, bmp)
            if (v.tag == path) v.setImageBitmap(bmp)
        }
    }

    // load — картинка не больше ~256 точек по стороне: логотипы бывают огромными.
    private fun load(path: String): Bitmap? = try {
        val c = URL(base + path.removePrefix("/")).openConnection() as HttpURLConnection
        c.connectTimeout = 5000
        c.readTimeout = 5000
        val bytes = try {
            if (c.responseCode == 200) c.inputStream.readBytes() else null
        } finally {
            c.disconnect()
        }
        bytes?.let {
            val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(it, 0, it.size, o)
            var sample = 1
            while (o.outWidth / (sample * 2) >= 256 || o.outHeight / (sample * 2) >= 256) sample *= 2
            BitmapFactory.decodeByteArray(it, 0, it.size, BitmapFactory.Options().apply { inSampleSize = sample })
        }
    } catch (e: Exception) {
        null
    }
}
