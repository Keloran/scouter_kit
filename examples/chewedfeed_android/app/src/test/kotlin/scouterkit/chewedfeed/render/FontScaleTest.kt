package scouterkit.chewedfeed.render

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import java.io.File
import java.security.MessageDigest

// The built-in font from src/base/departure_mono.inc, as the native library has it.
internal fun departureMono(): ByteArray {
    var dir: File? = File("").absoluteFile
    while (dir != null && !File(dir, "src/base/departure_mono.inc").isFile) dir = dir.parentFile
    val text = File(dir ?: error("src/base not found"), "src/base/departure_mono.inc").readText()
    val body = text.substring(text.indexOf('{') + 1, text.lastIndexOf('}'))
    return Regex("0x([0-9a-fA-F]{2})").findAll(body).map { it.groupValues[1].toInt(16).toByte() }.toList().toByteArray()
}

private fun sha(b: ByteArray) = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

class FontScaleTest {
    // The desktop's scaleFontBlob output for these scales (go test, sha256 and size).
    @Test
    fun sameBytesAsTheDesktop() {
        val src = departureMono()
        assertEquals("1525e7ead3d104e811c139ccfa3a8bb919034bd421587d93690352792508b76b", sha(src))
        for ((scale, want) in listOf(
            0.5 to "abba20127321e4eba931c21df25c4774e8265381b8fbcbd27891fa512efa595d",
            0.75 to "3d1ffbbad0a0cc4a4c7621a9f0c1b25bf85e33ff77297b13742bc8c693ffee7b",
            1.5 to "ba8180f53ec1df4d80b46d350dc65eed07998c5728f23fe46307edf5e2ffc212",
            2.25 to "79b5f36ac7c88e829e90db69c2945bed47f574a05766ae7c6f540e9a57a93b75",
        )) assertEquals("scale $scale", want, sha(FontScale.scale(src, scale)))
    }

    @Test
    fun refusesBadInput() {
        assertThrows(IllegalArgumentException::class.java) { FontScale.scale(departureMono(), 3.5) }
        assertThrows(IllegalArgumentException::class.java) { FontScale.scale(departureMono(), Double.NaN) }
        assertThrows(IllegalArgumentException::class.java) { FontScale.scale(ByteArray(10), 1.5) }
        val damaged = departureMono().copyOf(5000)
        assertThrows(IllegalArgumentException::class.java) { FontScale.scale(damaged, 1.5) }
    }
}
