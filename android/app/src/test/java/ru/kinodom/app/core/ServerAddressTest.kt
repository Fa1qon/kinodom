package ru.kinodom.app.core

import org.junit.Assert.assertEquals
import org.junit.Test

// Адрес Kinodom вводят по-разному (план 13a, Review Focus 3): понятный разбор или «неверный», без падения.
class ServerAddressTest {
    private fun ok(input: String, base: String) = assertEquals(input, Parsed.Ok(base), ServerAddress.parse(input))
    private fun bad(input: String) = assertEquals(input, Parsed.Bad, ServerAddress.parse(input))

    @Test
    fun ipWithoutPortGetsDefault() = ok("192.168.0.10", "http://192.168.0.10:8090/")

    @Test
    fun ipWithPort() = ok("192.168.0.10:8097", "http://192.168.0.10:8097/")

    @Test
    fun fullUrlWithSlash() = ok("http://192.168.0.10:8090/", "http://192.168.0.10:8090/")

    @Test
    fun urlWithPathKeepsOnlyServer() = ok("http://192.168.0.10:8090/#/catalog/rutor", "http://192.168.0.10:8090/")

    @Test
    fun spacesAreTrimmed() = ok("  192.168.0.10:8090  ", "http://192.168.0.10:8090/")

    @Test
    fun upperCaseScheme() = ok("HTTP://192.168.0.10", "http://192.168.0.10:8090/")

    @Test
    fun hostName() = ok("kinodom-pc.local", "http://kinodom-pc.local:8090/")

    @Test
    fun emulatorHost() = ok("10.0.2.2:8097", "http://10.0.2.2:8097/")

    @Test
    fun httpsIsNotOurs() = bad("https://192.168.0.10")

    @Test
    fun empty() = bad("   ")

    @Test
    fun spaceInside() = bad("abc def")

    @Test
    fun wrongOctet() = bad("192.168.0.300")

    @Test
    fun portZero() = bad("192.168.0.10:0")

    @Test
    fun portTooBig() = bad("192.168.0.10:70000")

    @Test
    fun portNotNumber() = bad("192.168.0.10:abc")

    @Test
    fun onlyPort() = bad(":8090")

    @Test
    fun otherScheme() = bad("ftp://192.168.0.10")
}
