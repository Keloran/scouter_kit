package scouterkit.chewedfeed.handover

import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction

// Every rejection of a frame or message (Go's ErrInvalid): a fixed, bounded reason.
class InvalidMessage(reason: String) : Exception("invalid message: $reason")

internal fun invalid(reason: String): Nothing = throw InvalidMessage(reason)

// A JSON number as written, so "integer" can mean what the Go and Python checks mean (no fraction or exponent).
data class JsonNumber(val text: String) {
    val double get() = text.toDouble()
    val isInteger get() = text.toLongOrNull() != null
    val long get() = text.toLong()
}

// The strict JSON of the handover protocol (wire.go strictJSON, contract.py strict_json): valid UTF-8, nesting up
// to 16, no duplicate keys, no lone surrogates, no control characters in strings or keys, finite numbers, nothing
// after the value. Objects are LinkedHashMaps (keys in order), arrays Lists, numbers JsonNumber.
object StrictJson {
    const val MAX_DEPTH = 16

    // allowControl: only for reading test fixtures that hold invalid messages as data.
    fun parse(bytes: ByteArray, maxBytes: Int, allowControl: Boolean = false): Any? {
        if (bytes.size > maxBytes) invalid("frame too large")
        val text = try {
            Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT).onUnmappableCharacter(CodingErrorAction.REPORT)
                .decode(ByteBuffer.wrap(bytes)).toString()
        } catch (e: CharacterCodingException) {
            invalid("invalid UTF-8")
        }
        return Parser(text, allowControl).run()
    }

    private class Parser(val s: String, val allowControl: Boolean) {
        var i = 0

        fun run(): Any? {
            val v = value(0)
            space()
            if (i != s.length) invalid("trailing data")
            return v
        }

        fun space() {
            while (i < s.length && s[i] in " \t\r\n") i++
        }

        fun value(depth: Int): Any? {
            space()
            if (i >= s.length) invalid("invalid JSON")
            return when (val c = s[i]) {
                '{' -> obj(depth + 1)
                '[' -> arr(depth + 1)
                '"' -> string()
                't' -> word("true", true)
                'f' -> word("false", false)
                'n' -> word("null", null)
                else -> if (c == '-' || c in '0'..'9') number() else invalid("invalid JSON")
            }
        }

        fun word(w: String, v: Any?): Any? {
            if (!s.startsWith(w, i)) invalid("invalid JSON")
            i += w.length
            return v
        }

        fun obj(depth: Int): Map<String, Any?> {
            if (depth > MAX_DEPTH) invalid("nesting too deep")
            i++
            val out = LinkedHashMap<String, Any?>()
            space()
            if (i < s.length && s[i] == '}') { i++; return out }
            while (true) {
                space()
                if (i >= s.length || s[i] != '"') invalid("invalid JSON")
                val key = string()
                if (out.containsKey(key)) invalid("duplicate key")
                space()
                if (i >= s.length || s[i] != ':') invalid("invalid JSON")
                i++
                out[key] = value(depth)
                space()
                if (i >= s.length) invalid("invalid JSON")
                when (s[i++]) {
                    ',' -> continue
                    '}' -> return out
                    else -> invalid("invalid JSON")
                }
            }
        }

        fun arr(depth: Int): List<Any?> {
            if (depth > MAX_DEPTH) invalid("nesting too deep")
            i++
            val out = ArrayList<Any?>()
            space()
            if (i < s.length && s[i] == ']') { i++; return out }
            while (true) {
                out += value(depth)
                space()
                if (i >= s.length) invalid("invalid JSON")
                when (s[i++]) {
                    ',' -> continue
                    ']' -> return out
                    else -> invalid("invalid JSON")
                }
            }
        }

        fun hex4(): Int {
            if (i + 4 > s.length) invalid("invalid JSON")
            val v = s.substring(i, i + 4).toIntOrNull(16) ?: invalid("invalid JSON")
            i += 4
            return v
        }

        fun string(): String {
            i++ // opening quote
            val out = StringBuilder()
            while (true) {
                if (i >= s.length) invalid("invalid JSON")
                val c = s[i++]
                when {
                    c == '"' -> break
                    c == '\\' -> {
                        if (i >= s.length) invalid("invalid JSON")
                        when (s[i++]) {
                            '"' -> out.append('"'); '\\' -> out.append('\\'); '/' -> out.append('/')
                            'b' -> out.append('\b'); 'f' -> out.append('\u000C'); 'n' -> out.append('\n')
                            'r' -> out.append('\r'); 't' -> out.append('\t')
                            'u' -> {
                                val u = hex4()
                                when (u) {
                                    in 0xDC00..0xDFFF -> invalid("unpaired surrogate")
                                    in 0xD800..0xDBFF -> {
                                        if (i + 2 > s.length || s[i] != '\\' || s[i + 1] != 'u') invalid("unpaired surrogate")
                                        i += 2
                                        val lo = hex4()
                                        if (lo !in 0xDC00..0xDFFF) invalid("unpaired surrogate")
                                        out.append(u.toChar()).append(lo.toChar())
                                    }
                                    else -> out.append(u.toChar())
                                }
                            }
                            else -> invalid("invalid JSON")
                        }
                    }
                    c < ' ' -> invalid("invalid JSON") // raw control characters are not JSON
                    else -> out.append(c)
                }
            }
            val r = out.toString()
            if (!allowControl && r.any { it < ' ' || it == '\u007F' }) invalid("control character")
            return r
        }

        fun number(): JsonNumber {
            val start = i
            if (s[i] == '-') i++
            if (i >= s.length) invalid("invalid JSON")
            if (s[i] == '0') i++ else if (s[i] in '1'..'9') { while (i < s.length && s[i].isDigit()) i++ } else invalid("invalid JSON")
            if (i < s.length && s[i] == '.') {
                i++
                if (i >= s.length || !s[i].isDigit()) invalid("invalid JSON")
                while (i < s.length && s[i].isDigit()) i++
            }
            if (i < s.length && (s[i] == 'e' || s[i] == 'E')) {
                i++
                if (i < s.length && (s[i] == '+' || s[i] == '-')) i++
                if (i >= s.length || !s[i].isDigit()) invalid("invalid JSON")
                while (i < s.length && s[i].isDigit()) i++
            }
            val n = JsonNumber(s.substring(start, i))
            if (n.double.isInfinite() || n.double.isNaN()) invalid("nonfinite number")
            return n
        }
    }

    // Compact JSON of a parsed value (numbers as written, keys in order). Strings are escaped as JSON requires.
    fun write(v: Any?): String = StringBuilder().also { write(v, it) }.toString()

    private fun write(v: Any?, out: StringBuilder) {
        when (v) {
            null -> out.append("null")
            is Boolean -> out.append(v)
            is JsonNumber -> out.append(v.text)
            is Int, is Long -> out.append(v.toString())
            is Double, is Float -> {
                val d = (v as Number).toDouble()
                require(!d.isNaN() && !d.isInfinite()) { "not a finite JSON number" }
                out.append(v.toString())
            }
            is String -> {
                out.append('"')
                for (c in v) when {
                    c == '"' -> out.append("\\\"")
                    c == '\\' -> out.append("\\\\")
                    c < ' ' || c == '\u007F' -> out.append(String.format("\\u%04x", c.code))
                    else -> out.append(c)
                }
                out.append('"')
            }
            is Map<*, *> -> {
                out.append('{')
                var first = true
                for ((k, value) in v) {
                    if (!first) out.append(',')
                    first = false
                    write(k as String, out)
                    out.append(':')
                    write(value, out)
                }
                out.append('}')
            }
            is List<*> -> {
                out.append('[')
                v.forEachIndexed { n, item -> if (n > 0) out.append(','); write(item, out) }
                out.append(']')
            }
            else -> throw IllegalArgumentException("not a JSON value: ${v::class.java.simpleName}")
        }
    }
}
