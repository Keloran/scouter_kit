package scouterkit.chewedfeed.settings

import android.content.Context

// The few typed reads and writes the settings need, so they can be tested without Android.
interface KeyValue {
    fun has(key: String): Boolean
    fun int(key: String, default: Int): Int
    fun float(key: String, default: Float): Float
    fun bool(key: String, default: Boolean): Boolean
    fun string(key: String, default: String): String
    fun edit(block: Editor.() -> Unit)

    interface Editor {
        fun int(key: String, v: Int)
        fun float(key: String, v: Float)
        fun bool(key: String, v: Boolean)
        fun string(key: String, v: String)
        fun remove(key: String)
    }
}

class Prefs(context: Context, name: String) : KeyValue {
    private val p = context.getSharedPreferences(name, Context.MODE_PRIVATE)
    override fun has(key: String) = p.contains(key)
    override fun int(key: String, default: Int) = runCatching { p.getInt(key, default) }.getOrDefault(default)
    override fun float(key: String, default: Float) = runCatching { p.getFloat(key, default) }.getOrDefault(default)
    override fun bool(key: String, default: Boolean) = runCatching { p.getBoolean(key, default) }.getOrDefault(default)
    override fun string(key: String, default: String) = runCatching { p.getString(key, default) ?: default }.getOrDefault(default)
    override fun edit(block: KeyValue.Editor.() -> Unit) {
        val e = p.edit()
        object : KeyValue.Editor {
            override fun int(key: String, v: Int) { e.putInt(key, v) }
            override fun float(key: String, v: Float) { e.putFloat(key, v) }
            override fun bool(key: String, v: Boolean) { e.putBoolean(key, v) }
            override fun string(key: String, v: String) { e.putString(key, v) }
            override fun remove(key: String) { e.remove(key) }
        }.block()
        e.apply()
    }
}
