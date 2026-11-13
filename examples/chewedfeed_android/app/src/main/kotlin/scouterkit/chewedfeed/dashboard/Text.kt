package scouterkit.chewedfeed.dashboard

// Text fitting, as the desktop dashboard does it (go/examples/chewedfeed pages.go): measured in pixels by the
// caller's font, split only between code points (never inside a surrogate pair).

private fun String.codePointList(): List<String> {
    val out = ArrayList<String>(length)
    var i = 0
    while (i < length) {
        val n = Character.charCount(codePointAt(i))
        out += substring(i, i + n)
        i += n
    }
    return out
}

// Word-wraps one paragraph to width; a word longer than a line is cut.
fun wrap(text: String, width: Int, measure: (String) -> Int): List<String> {
    if (text.isEmpty()) return listOf("")
    val lines = mutableListOf<String>()
    var s = text
    while (s.isNotEmpty()) {
        val r = s.codePointList()
        var n = 0
        var space = 0
        while (n < r.size && measure(r.subList(0, n + 1).joinToString("")) <= width) {
            n++
            if (r[n - 1] == " ") space = n
        }
        if (n == r.size) {
            lines += s.trim()
            break
        }
        if (space > 0) n = space
        n = maxOf(1, n)
        lines += r.subList(0, n).joinToString("").trim()
        s = r.subList(n, r.size).joinToString("").trim()
    }
    return lines
}

// Keeps text on one line, ending in "..." when something was left out.
fun fitLine(text: String, width: Int, measure: (String) -> Int): String {
    if (measure(text) <= width) return text
    var dots = "..."
    while (dots.isNotEmpty() && measure(dots) > width) dots = dots.dropLast(1)
    var r = text.codePointList()
    while (r.isNotEmpty() && measure(r.joinToString("").trim() + dots) > width) r = r.dropLast(1)
    return r.joinToString("").trim() + dots
}
