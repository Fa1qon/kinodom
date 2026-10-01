package ru.kinodom.app.net

import android.content.Context
import android.net.wifi.WifiManager
import java.net.DatagramPacket
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.MulticastSocket
import java.net.SocketTimeoutException
import java.net.URL
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.withContext
import ru.kinodom.app.core.Found
import ru.kinodom.app.core.Ssdp

// ServerFinder — Kinodom в домашней сети по SSDP (спека этапа 13, раздел 3.2): M-SEARCH в группу, ответы за
// timeoutMs, описание каждого ответившего. Многоадресный приём на Wi-Fi Android открывает только под MulticastLock.
// Сети нет (ТВ только включили, телефон в режиме полёта) — отправка бросает ENETUNREACH: никого не нашли.
class ServerFinder(private val context: Context) {
    suspend fun find(timeoutMs: Long = 5000): List<Found> = withContext(Dispatchers.IO) {
        val wifi = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager?
        val lock = wifi?.createMulticastLock("kinodom-ssdp")?.apply { setReferenceCounted(false); acquire() }
        try {
            val locations = try {
                search(timeoutMs)
            } catch (e: Exception) {
                emptyList()
            }
            coroutineScope { locations.map { async { describe(it) } }.awaitAll() }.filterNotNull().distinctBy { it.base }
        } finally {
            lock?.release()
        }
    }

    // search — адреса описаний из ответов Kinodom; запрос повторяется через секунду (UDP теряется).
    private fun search(timeoutMs: Long): List<String> {
        val out = LinkedHashSet<String>()
        MulticastSocket().use { s ->
            val group = InetAddress.getByName(Ssdp.GROUP)
            val req = Ssdp.search()
            val deadline = System.currentTimeMillis() + timeoutMs
            var nextSend = 0L
            val buf = ByteArray(2048)
            while (true) {
                val now = System.currentTimeMillis()
                if (now >= deadline) break
                if (now >= nextSend) {
                    s.send(DatagramPacket(req, req.size, group, Ssdp.PORT))
                    nextSend = now + 1000
                }
                s.soTimeout = (minOf(deadline, nextSend) - now).toInt().coerceAtLeast(1)
                val p = DatagramPacket(buf, buf.size)
                try {
                    s.receive(p)
                } catch (e: SocketTimeoutException) {
                    continue
                }
                Ssdp.location(String(p.data, 0, p.length, Charsets.UTF_8))?.let { out += it }
            }
        }
        return out.toList()
    }

    private fun describe(location: String): Found? = try {
        val c = URL(location).openConnection() as HttpURLConnection
        c.connectTimeout = 2000
        c.readTimeout = 2000
        try {
            if (c.responseCode == 200) Ssdp.parseDesc(c.inputStream.bufferedReader().readText()) else null
        } finally {
            c.disconnect()
        }
    } catch (e: Exception) {
        null
    }
}
