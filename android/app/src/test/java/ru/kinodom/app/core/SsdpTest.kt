package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// Поиск Kinodom по SSDP (спека этапа 13, раздел 3.2): ответы чужих устройств (DLNA-сервер, роутер) — мимо,
// описание — имя ПК и адрес пульта.
class SsdpTest {
    private val kinodomReply = "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\n" +
        "LOCATION: http://192.168.0.26:8090/upnp/desc.xml\r\nSERVER: Kinodom/0.11.0\r\n" +
        "ST: urn:kinodom-ru:device:Kinodom:1\r\nUSN: uuid:6b1f::urn:kinodom-ru:device:Kinodom:1\r\n\r\n"

    @Test
    fun searchAsksForKinodom() {
        val s = String(Ssdp.search(), Charsets.US_ASCII)
        assertTrue(s, s.startsWith("M-SEARCH * HTTP/1.1\r\n"))
        assertTrue(s, s.contains("\r\nMAN: \"ssdp:discover\"\r\n"))
        assertTrue(s, s.contains("\r\nST: urn:kinodom-ru:device:Kinodom:1\r\n"))
        assertTrue(s, s.contains("\r\nHOST: 239.255.255.250:1900\r\n"))
        assertTrue(s, s.endsWith("\r\n\r\n"))
    }

    @Test
    fun locationOfKinodom() = assertEquals("http://192.168.0.26:8090/upnp/desc.xml", Ssdp.location(kinodomReply))

    @Test
    fun lowerCaseHeaders() = assertEquals(
        "http://10.0.0.5:8097/upnp/desc.xml",
        Ssdp.location("HTTP/1.1 200 OK\r\nlocation: http://10.0.0.5:8097/upnp/desc.xml\r\nst: urn:kinodom-ru:device:Kinodom:1\r\n\r\n"),
    )

    @Test
    fun foreignDeviceIgnored() = assertNull(
        Ssdp.location("HTTP/1.1 200 OK\r\nLOCATION: http://192.168.0.1:5000/rootDesc.xml\r\nST: urn:schemas-upnp-org:device:MediaServer:1\r\n\r\n"),
    )

    @Test
    fun notifyIgnored() = assertNull(
        Ssdp.location("NOTIFY * HTTP/1.1\r\nLOCATION: http://192.168.0.26:8090/upnp/desc.xml\r\nNT: urn:kinodom-ru:device:Kinodom:1\r\n\r\n"),
    )

    @Test
    fun garbageIgnored() = assertNull(Ssdp.location("\u0000\u0001"))

    private fun desc(name: String, presentation: String?, type: String = "urn:kinodom-ru:device:Kinodom:1") =
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root xmlns=\"urn:schemas-upnp-org:device-1-0\">" +
            "<specVersion><major>1</major><minor>0</minor></specVersion><device><deviceType>$type</deviceType>" +
            "<friendlyName>$name</friendlyName><manufacturer>Kinodom</manufacturer><UDN>uuid:6b1f</UDN>" +
            (presentation?.let { "<presentationURL>$it</presentationURL>" } ?: "") + "</device></root>"

    @Test
    fun descGivesNameAndBase() = assertEquals(
        Found("http://192.168.0.26:8090/", "Kinodom на DESKTOP-PC"),
        Ssdp.parseDesc(desc("Kinodom на DESKTOP-PC", "http://192.168.0.26:8090/")),
    )

    @Test
    fun descEscapedName() = assertEquals(
        "Kinodom на ПК <Семья> & \"дом\"",
        Ssdp.parseDesc(desc("Kinodom на ПК &lt;Семья&gt; &amp; &quot;дом&quot;", "http://192.168.0.26:8090/"))?.name,
    )

    @Test
    fun descBaseGetsSlash() = assertEquals("http://192.168.0.26:8090/", Ssdp.parseDesc(desc("K", "http://192.168.0.26:8090"))?.base)

    @Test
    fun descWithoutPresentation() = assertNull(Ssdp.parseDesc(desc("K", null)))

    @Test
    fun descOfOtherDevice() = assertNull(Ssdp.parseDesc(desc("Роутер", "http://192.168.0.1/", "urn:schemas-upnp-org:device:InternetGatewayDevice:1")))

    @Test
    fun descNotXml() = assertNull(Ssdp.parseDesc("<html>404"))
}
