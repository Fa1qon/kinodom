package ru.kinodom.app.core

import java.io.ByteArrayInputStream
import javax.xml.parsers.DocumentBuilderFactory
import org.w3c.dom.Element

// Found — найденный сервер: адрес пульта («http://…/») и имя («Kinodom на <ПК>»).
data class Found(val base: String, val name: String)

// Ssdp — поиск Kinodom в домашней сети (спека этапа 13, раздел 3.2): запрос M-SEARCH, разбор ответов и описания
// устройства. Без Android — проверяется тестами на JVM; сеть — ServerFinder.
object Ssdp {
    const val ST = "urn:kinodom-ru:device:Kinodom:1"
    const val GROUP = "239.255.255.250"
    const val PORT = 1900

    // search — запрос поиска Kinodom; MX 2 — ответ в пределах двух секунд.
    fun search(): ByteArray = (
        "M-SEARCH * HTTP/1.1\r\n" +
            "HOST: $GROUP:$PORT\r\n" +
            "MAN: \"ssdp:discover\"\r\n" +
            "MX: 2\r\n" +
            "ST: $ST\r\n\r\n"
        ).toByteArray(Charsets.US_ASCII)

    // location — адрес описания из ответа Kinodom; ответ чужого устройства, объявление, мусор — null.
    fun location(response: String): String? {
        val lines = response.split("\r\n", "\n")
        if (lines.isEmpty() || !lines[0].startsWith("HTTP/1.1 200")) return null
        val headers = HashMap<String, String>()
        for (line in lines.drop(1)) {
            val colon = line.indexOf(':')
            if (colon > 0) headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
        }
        if (headers["st"] != ST) return null
        return headers["location"]?.takeIf { it.startsWith("http://") }
    }

    // parseDesc — имя и адрес пульта из описания устройства Kinodom; другое устройство, нет адреса, не XML — null.
    fun parseDesc(xml: String): Found? {
        val doc = try {
            DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(ByteArrayInputStream(xml.toByteArray(Charsets.UTF_8)))
        } catch (e: Exception) {
            return null
        }
        val device = doc.getElementsByTagName("device").item(0) as? Element ?: return null
        fun text(tag: String) = device.getElementsByTagName(tag).item(0)?.textContent?.trim().orEmpty()
        if (text("deviceType") != ST) return null
        val base = text("presentationURL").takeIf { it.startsWith("http://") } ?: return null
        return Found(if (base.endsWith("/")) base else "$base/", text("friendlyName").ifEmpty { base })
    }
}
