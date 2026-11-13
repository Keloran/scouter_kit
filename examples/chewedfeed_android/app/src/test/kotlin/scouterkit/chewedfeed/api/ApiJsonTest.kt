package scouterkit.chewedfeed.api

import org.junit.Assert.*
import org.junit.Test

class ApiJsonTest {
    @Test fun strictJsonAndStructuralLimits() {
        val valid = ApiJson("""{"text":"Café \u20ac","value":-1.25e2,"items":[null,true,false]}""").parse() as Map<*, *>
        assertEquals("Café €", valid["text"])
        assertEquals(-125.0, valid["value"])
        for (bad in listOf("{unquoted:1}", "{'x':1}", "[1,]", "{\"x\":1,}", "{\"x\":1,\"x\":2}", "+1", "01", ".1", "1e9999", "true false", "\"\\u+123\"", "[".repeat(34) + "0" + "]".repeat(34))) {
            try { ApiJson(bad).parse(); fail("Accepted invalid JSON") } catch (_: IllegalArgumentException) { } catch (_: IllegalStateException) { }
        }
    }

    @Test fun unsafeConfigurationIsRejectedWithoutEchoingIt() {
        for (base in listOf("", "ftp://example.invalid", "http://example.invalid", "https://user:test-session@example.invalid", "https://example.invalid?token=test-session", "https://example.invalid#test-session", "https://example.invalid:99999")) {
            try { ApiConfig.create(base); fail("Unsafe base URL accepted") } catch (e: IllegalArgumentException) {
                assertFalse(e.toString().contains("test-session"))
            }
        }
        assertEquals("https", ApiConfig.create("https://example.invalid/api/").base.scheme)
        try { ApiConfig.create("https://example.invalid", project = "../private"); fail("Invalid slug") } catch (_: IllegalArgumentException) { }
        try { ApiConfig.create("https://example.invalid", requestTimeoutMs = 0); fail("Invalid timeout") } catch (_: IllegalArgumentException) { }
    }
}
