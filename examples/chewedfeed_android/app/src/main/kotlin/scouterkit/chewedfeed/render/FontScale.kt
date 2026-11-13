package scouterkit.chewedfeed.render

import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.math.abs
import kotlin.math.floor

// Resamples a 1-bpp font blob (font_converter's SKF1) to a fractional scale, byte for byte as the desktop does it
// (go/examples/chewedfeed font.go scaleFontBlob), because the native loader's scale is whole numbers only.
object FontScale {
    const val MIN = 0.1
    const val MAX = 3.0

    // Go's math.Round: halves away from zero.
    private fun goRound(v: Double): Int = (if (v < 0) -floor(abs(v) + 0.5) else floor(v + 0.5)).toInt()

    fun scale(src: ByteArray, scale: Double): ByteArray {
        require(scale in MIN..MAX && !scale.isNaN()) { "text scale must be between $MIN and $MAX" }
        val b = ByteBuffer.wrap(src).order(ByteOrder.LITTLE_ENDIAN)
        require(src.size >= 16 && src[0] == 'S'.code.toByte() && src[1] == 'K'.code.toByte() && src[2] == 'F'.code.toByte() && src[3] == 1.toByte()) { "invalid font blob" }
        val n = b.getShort(8).toInt() and 0xFFFF
        val k = b.getShort(10).toInt() and 0xFFFF
        val head = 16 + 16 * n + 5 * k
        require(n != 0 && head <= src.size && b.getInt(4).toLong() and 0xFFFFFFFFL == src.size.toLong()) { "invalid font blob" }
        val out = java.io.ByteArrayOutputStream(src.size * 2)
        out.write(src, 0, head)
        val bytes = out.toByteArray().toMutableList()
        fun round(v: Int) = goRound(v * scale)
        for (i in 12..14) bytes[i] = minOf(255, round(src[i].toInt() and 0xFF)).toByte()
        bytes[14] = maxOf(1, bytes[14].toInt() and 0xFF).toByte() // the dashboard divides by line spacing
        val tail = ArrayList<Byte>()
        for (i in 0 until n) {
            val off = 16 + 12 * i
            val bits = b.getInt(off).toLong() and 0xFFFFFFFFL
            val w = src[off + 4].toInt() and 0xFF
            val h = src[off + 5].toInt() and 0xFF
            require(bits + w.toLong() * h <= src.size.toLong() * 8) { "invalid font blob" }
            val nw = round(w)
            val nh = round(h)
            val advance = round(src[off + 6].toInt() and 0xFF)
            require(nw <= 255 && nh <= 255 && advance <= 255) { "scaled font glyph exceeds 255 pixels" }
            val start = (head + tail.size).toLong() * 8
            putInt(bytes, off, start.toInt())
            bytes[off + 4] = nw.toByte(); bytes[off + 5] = nh.toByte(); bytes[off + 6] = advance.toByte()
            for (j in intArrayOf(8, 10)) {
                val v = round(b.getShort(off + j).toInt())
                require(v in -32768..32767) { "scaled font offset exceeds signed 16-bit range" }
                bytes[off + j] = v.toByte(); bytes[off + j + 1] = (v shr 8).toByte()
            }
            val pixels = ByteArray((nw * nh + 7) / 8)
            if (w > 0) for (y in 0 until nh) for (x in 0 until nw) {
                val sx = minOf(w - 1, (x / scale).toInt())
                val sy = minOf(h - 1, (y / scale).toInt())
                val bit = bits + sy * w + sx
                if ((src[(bit / 8).toInt()].toInt() and (1 shl (7 - (bit % 8).toInt()))) != 0) {
                    val p = y * nw + x
                    pixels[p / 8] = (pixels[p / 8].toInt() or (1 shl (7 - p % 8))).toByte()
                }
            }
            for (p in pixels) tail += p
        }
        for (i in 0 until k) {
            val off = 16 + 16 * n + 4 * k + i
            bytes[off] = maxOf(-128, minOf(127, round(src[off].toInt()))).toByte()
        }
        val result = ByteArray(bytes.size + tail.size)
        for (i in bytes.indices) result[i] = bytes[i]
        for (i in tail.indices) result[bytes.size + i] = tail[i]
        putInt(result, 4, result.size)
        return result
    }

    private fun putInt(a: MutableList<Byte>, off: Int, v: Int) {
        for (i in 0..3) a[off + i] = (v shr (8 * i)).toByte()
    }

    private fun putInt(a: ByteArray, off: Int, v: Int) {
        for (i in 0..3) a[off + i] = (v shr (8 * i)).toByte()
    }
}
