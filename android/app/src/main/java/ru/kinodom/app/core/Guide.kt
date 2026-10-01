package ru.kinodom.app.core

import java.util.Calendar
import java.util.GregorianCalendar
import java.util.Locale
import java.util.TimeZone
import org.json.JSONObject

// Rfc3339 — время из JSON сервера («2026-10-01T21:00:00+07:00», «…Z», с долями секунды) в мс UTC; java.time
// не берётся — его нет до Android 8.
object Rfc3339 {
    private val re = Regex("""(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})""")

    fun parse(s: String): Long? {
        val m = re.matchEntire(s.trim()) ?: return null
        val g = m.groupValues
        val c = GregorianCalendar(TimeZone.getTimeZone("UTC"), Locale.ROOT)
        c.clear()
        c.set(g[1].toInt(), g[2].toInt() - 1, g[3].toInt(), g[4].toInt(), g[5].toInt(), g[6].toInt())
        var ms = c.timeInMillis
        if (g[7].isNotEmpty()) ms += (("0" + g[7]).toDouble() * 1000).toLong()
        if (g[8] != "Z") {
            val sign = if (g[8][0] == '-') -1 else 1
            val off = g[8].substring(1).split(":").let { it[0].toInt() * 60 + it[1].toInt() }
            ms -= sign * off * 60_000L
        }
        return ms
    }
}

// Prog — передача: начало и конец в мс UTC.
data class Prog(val start: Long, val stop: Long, val title: String)

// Epg — программа канала из GET /api/v1/channels/{версия}/epg (спека этапа 13, 4.4): передачи по времени и пояс
// каналов (часы от UTC), по которому их показывать.
data class Epg(val items: List<Prog>, val utcOffset: Int) {
    fun now(at: Long): Prog? = items.firstOrNull { it.start <= at && at < it.stop }

    // next — первая после текущей; текущей нет (перерыв) — первая после at.
    fun next(at: Long): Prog? {
        val after = now(at)?.stop ?: (at + 1)
        return items.firstOrNull { it.start >= after }
    }

    // plus — сегодня и завтра одной программой (последняя передача дня: «Дальше» — с завтрашней).
    operator fun plus(other: Epg): Epg = Epg((items + other.items).distinctBy { it.start }.sortedBy { it.start }, utcOffset)

    companion object {
        fun parse(json: String): Epg? = try {
            val o = JSONObject(json)
            val arr = o.getJSONArray("items")
            val out = ArrayList<Prog>()
            for (i in 0 until arr.length()) {
                val p = arr.optJSONObject(i) ?: continue
                val s = Rfc3339.parse(p.optString("start", "")) ?: continue
                val e = Rfc3339.parse(p.optString("stop", "")) ?: continue
                out += Prog(s, e, p.optString("title", ""))
            }
            Epg(out.sortedBy { it.start }, o.optInt("utcOffset", 3))
        } catch (e: Exception) {
            null
        }
    }
}

object Guide {
    // needTomorrow — программа есть, а следующей передачи сегодня нет — спросить завтрашнюю.
    fun needTomorrow(today: Epg, at: Long): Boolean = today.items.isNotEmpty() && today.next(at) == null

    // date — «2026-10-02» через days дней по поясу каналов (для ?date= программы).
    fun date(days: Int, utcOffset: Int, at: Long): String {
        val c = zoned(at, utcOffset)
        c.add(Calendar.DAY_OF_MONTH, days)
        return String.format(Locale.ROOT, "%04d-%02d-%02d", c.get(Calendar.YEAR), c.get(Calendar.MONTH) + 1, c.get(Calendar.DAY_OF_MONTH))
    }

    fun hhmm(t: Long, utcOffset: Int): String {
        val c = zoned(t, utcOffset)
        return String.format(Locale.ROOT, "%02d:%02d", c.get(Calendar.HOUR_OF_DAY), c.get(Calendar.MINUTE))
    }

    fun progress(p: Prog, at: Long): Int {
        if (p.stop <= p.start) return 0
        return (((at - p.start) * 100) / (p.stop - p.start)).coerceIn(0, 100).toInt()
    }

    // zoned — «часы на стене» в поясе UTC+offset: календарь UTC, сдвинутый на offset.
    private fun zoned(t: Long, utcOffset: Int): Calendar =
        GregorianCalendar(TimeZone.getTimeZone("UTC"), Locale.ROOT).apply { timeInMillis = t + utcOffset * 3_600_000L }
}
