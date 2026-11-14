package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Test

// The LAN advertisement lives exactly as long as the listener, on its real port, whoever opened it.
class LaptopHubTest {
    @Test
    fun advertisesWhileOpen() {
        val ports = ArrayList<Int>()
        var closed = 0
        val hub = LaptopHub(memoryStore(), port = 0, advertise = { p -> ports += p; AutoCloseable { closed++ } })
        hub.open("service")
        hub.open("pairing")
        assertEquals(1, ports.size)
        assert(ports[0] > 0)
        hub.close("pairing")
        assertEquals(0, closed)
        hub.close("service")
        assertEquals(1, closed)
        hub.open("service")
        assertEquals(2, ports.size)
        hub.close("service")
    }
}
