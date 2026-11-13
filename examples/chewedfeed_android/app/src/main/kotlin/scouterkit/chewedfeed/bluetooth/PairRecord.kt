package scouterkit.chewedfeed.bluetooth

// The arms' pair record (g1_firmware PROTOCOL.md §2.1): manufacturer data without a company ID (0xFFFF), format 1,
// flags (bit 0 left arm, bit 1 no panel), then the pair ID both arms of one pair share. Pairs are identified by
// it, never by the advertised name.
data class PairRecord(val pairId: String, val left: Boolean, val hasPanel: Boolean) {
    companion object {
        const val MANUFACTURER_ID = 0xFFFF

        fun parse(data: ByteArray?): PairRecord? {
            if (data == null || data.size < 3 || data[0].toInt() != 1) return null
            val flags = data[1].toInt()
            val id = String(data, 2, data.size - 2, Charsets.UTF_8)
            if (id.isBlank() || id.any { it.isISOControl() }) return null
            return PairRecord(id, flags and 1 != 0, flags and 2 == 0)
        }
    }
}

// Groups scan results into pairs; only a pair with both arms, both with a panel, can be chosen.
class PairFinder {
    data class Pair(val pairId: String, val left: String?, val right: String?) {
        val complete get() = left != null && right != null
    }

    private val seen = LinkedHashMap<String, Pair>()

    fun add(address: String, record: PairRecord?) {
        if (record == null || !record.hasPanel) return
        val p = seen[record.pairId] ?: Pair(record.pairId, null, null)
        seen[record.pairId] = if (record.left) p.copy(left = address) else p.copy(right = address)
    }

    val pairs get() = seen.values.toList()
}
