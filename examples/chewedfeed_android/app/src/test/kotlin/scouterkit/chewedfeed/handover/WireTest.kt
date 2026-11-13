package scouterkit.chewedfeed.handover

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.io.File

internal fun handoverFile(name: String): File {
    var dir: File? = File("").absoluteFile
    while (dir != null && !File(dir, "tests/handover/$name").isFile) dir = dir.parentFile
    return File(dir ?: error("tests/handover not found"), "tests/handover/$name")
}

internal fun handoverFixture(name: String) = StrictJson.parse(handoverFile(name).readBytes(), 1 shl 24, allowControl = true) as Map<*, *>

private fun frameOf(payload: ByteArray) =
    byteArrayOf((payload.size ushr 24).toByte(), (payload.size ushr 16).toByte(), (payload.size ushr 8).toByte(), payload.size.toByte()) + payload

private fun read(bytes: ByteArray) = Wire.readFrame(ByteArrayInputStream(bytes))

// go/examples/internal/handover wire_test.go, on the shared fixtures.
class WireTest {
    @Test
    fun schemaIsTheSharedOne() {
        val copy = WireTest::class.java.getResourceAsStream("/handover/protocol.schema.json")!!.readBytes()
        assertArrayEquals(handoverFile("protocol.schema.json").readBytes(), copy)
    }

    @Test
    fun messageFixtures() {
        val f = handoverFixture("messages.json")
        val valid = f["valid"] as List<*>
        val bad = f["invalid"] as List<*>
        assertTrue(valid.size >= 10 && bad.size >= 20)
        for (c in valid) {
            c as Map<*, *>
            val payload = StrictJson.write(c["message"]).toByteArray()
            val m = read(frameOf(payload))
            val out = ByteArrayOutputStream()
            Wire.writeFrame(out, m)
            assertEquals(c["name"].toString(), StrictJson.write(c["message"]), String(out.toByteArray().copyOfRange(4, out.size())))
        }
        for (c in bad) {
            c as Map<*, *>
            val payload = StrictJson.write(c["message"]).toByteArray()
            assertThrows(c["name"].toString(), InvalidMessage::class.java) { read(frameOf(payload)) }
        }
    }

    @Test
    fun streamFixtures() {
        val f = handoverFixture("sequences.json")
        val id = f["identities"] as Map<*, *>
        val streams = f["streams"] as List<*>
        assertTrue(streams.size >= 15)
        for (c in streams) {
            c as Map<*, *>
            val o = c["options"] as Map<*, *>
            val s = Stream(id["sender"] as String, id["recipient"] as String, id["local_session"] as String,
                (o["retired"] as List<*>?).orEmpty().map { it as String }, o["pending"] as String? ?: "",
                (o["local_capabilities"] as List<*>?)?.map { it as String } ?: Wire.REQUIRED + Wire.OPTIONAL)
            for ((i, step) in (c["steps"] as List<*>).withIndex()) {
                step as Map<*, *>
                val ok = runCatching { s.receive(read(frameOf(StrictJson.write(step["message"]).toByteArray()))) }
                assertEquals("${c["name"]} step $i: ${ok.exceptionOrNull()?.message}", step["accept"], ok.isSuccess)
            }
        }
    }

    @Test
    fun frameErrors() {
        val hello = StrictJson.write(((handoverFixture("messages.json")["valid"] as List<*>)[0] as Map<*, *>)["message"])
        assertTrue(hello.contains("\"seq\":1,"))
        for ((name, data) in mapOf(
            "zero length" to byteArrayOf(0, 0, 0, 0),
            "oversized" to frameOf(ByteArray(0)).also { }.let { byteArrayOf(0, 1, 0, 1) },
            "duplicate key" to frameOf(hello.replace("\"seq\":1", "\"seq\":1,\"seq\":2").toByteArray()),
            "trailing" to frameOf("$hello {}".toByteArray()),
            "lone surrogate" to frameOf(hello.replace("\"cccccccccccccccccccccccccccccccc\"", "\"\\ud800ccccccccccccccccccccccccccccccc\"").toByteArray()),
            "deep" to frameOf(("[".repeat(17) + "]".repeat(17)).toByteArray()),
            "bad utf8" to frameOf(byteArrayOf('"'.code.toByte(), 0xff.toByte(), '"'.code.toByte())),
            "float seq" to frameOf(hello.replace("\"seq\":1", "\"seq\":1.0").toByteArray()),
            "huge number" to frameOf(hello.replace("\"epoch\":0", "\"epoch\":1e400").toByteArray()),
            "control character" to frameOf(hello.replace("\"hello\"", "\"hel\\u0001lo\"").toByteArray()),
        )) assertThrows(name, InvalidMessage::class.java) { read(data) }
        // Cut anywhere: an error, never a message.
        val full = frameOf(hello.toByteArray())
        for (n in 0 until full.size) {
            try {
                read(full.copyOf(n))
                fail("truncated at $n accepted")
            } catch (e: EOFException) {
            }
        }
        assertEquals("hello", read(full).kind)
    }

    @Test
    fun outboxAndOwnChecks() {
        val o = Outbox("a".repeat(64), "b".repeat(64), "c".repeat(32))
        val m1 = o.next("hello", 0, "", linkedMapOf("role" to "phone", "required_capabilities" to Wire.REQUIRED, "optional_capabilities" to Wire.OPTIONAL))
        val m2 = o.next("heartbeat", 3, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0))
        val m3 = o.next("heartbeat", 1, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0))
        assertEquals(listOf(1L, 2L, 3L), listOf(m1.seq, m2.seq, m3.seq))
        assertEquals(3L, m3.epoch) // never decreases
        assertEquals("hello", Wire.decode(Wire.encode(m1)).kind)
        // A message this host may not send fails here, not on the peer.
        val bad = o.next("ready", 3, "t".repeat(32), linkedMapOf("target" to "phone", "arms" to mapOf("left" to true, "right" to false), "renderer_ready" to true))
        assertThrows(InvalidMessage::class.java) { Wire.writeFrame(ByteArrayOutputStream(), bad) }
        assertFalse(o.toString().isEmpty())
    }
}
