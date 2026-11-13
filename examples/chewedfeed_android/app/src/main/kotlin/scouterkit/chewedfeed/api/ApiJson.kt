package scouterkit.chewedfeed.api

// Strict, bounded JSON for untrusted API responses; values match PageFormatter's
// maps/lists/Double model. No lenient Android JSONTokener extensions.
internal class ApiJson(private val text: String) {
    private var position = 0
    private var values = 0

    fun parse(): Any? {
        val result = value(0)
        space()
        require(position == text.length)
        return result
    }

    private fun space() {
        while (position < text.length && text[position] in " \r\n\t") position++
    }

    private fun consume(c: Char): Boolean {
        space()
        if (position < text.length && text[position] == c) { position++; return true }
        return false
    }

    private fun value(depth: Int): Any? {
        require(depth <= 32 && ++values <= 100_000)
        space()
        require(position < text.length)
        return when (text[position]) {
            '{' -> {
                position++
                val result = linkedMapOf<String, Any?>()
                if (!consume('}')) {
                    do {
                        space()
                        val key = string()
                        require(!result.containsKey(key) && consume(':'))
                        result[key] = value(depth + 1)
                    } while (consume(','))
                    require(consume('}'))
                }
                result
            }
            '[' -> {
                position++
                val result = mutableListOf<Any?>()
                if (!consume(']')) {
                    do { result += value(depth + 1) } while (consume(','))
                    require(consume(']'))
                }
                result
            }
            '"' -> string()
            't' -> literal("true", true)
            'f' -> literal("false", false)
            'n' -> literal("null", null)
            else -> {
                val start = position
                while (position < text.length && text[position] in "-+0123456789.eE") position++
                val raw = text.substring(start, position)
                require(raw.length in 1..128 && NUMBER.matches(raw))
                raw.toDouble().also { require(it.isFinite()) }
            }
        }
    }

    private fun literal(word: String, result: Any?): Any? {
        require(text.startsWith(word, position))
        position += word.length
        return result
    }

    private fun string(): String {
        require(position < text.length && text[position++] == '"')
        val out = StringBuilder()
        while (position < text.length) {
            val c = text[position++]
            if (c == '"') return out.toString()
            require(c >= ' ')
            if (c != '\\') { out.append(c); continue }
            require(position < text.length)
            when (val escaped = text[position++]) {
                '"', '\\', '/' -> out.append(escaped)
                'b' -> out.append('\b')
                'f' -> out.append('\u000c')
                'n' -> out.append('\n')
                'r' -> out.append('\r')
                't' -> out.append('\t')
                'u' -> {
                    require(position + 4 <= text.length)
                    val digits = text.substring(position, position + 4)
                    require(digits.all { it in "0123456789abcdefABCDEF" })
                    val unit = digits.toIntOrNull(16)
                    require(unit != null)
                    out.append(unit.toChar())
                    position += 4
                }
                else -> error("invalid escape")
            }
        }
        error("unterminated string")
    }

    companion object { private val NUMBER = Regex("-?(0|[1-9][0-9]*)(\\.[0-9]+)?([eE][+-]?[0-9]+)?") }
}
