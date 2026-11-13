package scouterkit.chewedfeed.settings

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test
import scouterkit.chewedfeed.DisplaySettings
import scouterkit.chewedfeed.Nudge
import scouterkit.chewedfeed.dashboard.DisplayOptions
import scouterkit.chewedfeed.nudge
import scouterkit.chewedfeed.service.Holder
import scouterkit.chewedfeed.service.OwnershipView
import scouterkit.chewedfeed.service.Phase
import scouterkit.chewedfeed.service.Progress
import scouterkit.chewedfeed.service.ownershipView

// SharedPreferences, in memory.
class MemoryKv : KeyValue {
    val map = mutableMapOf<String, Any>()
    override fun has(key: String) = key in map
    override fun int(key: String, default: Int) = map[key] as? Int ?: default
    override fun float(key: String, default: Float) = map[key] as? Float ?: default
    override fun bool(key: String, default: Boolean) = map[key] as? Boolean ?: default
    override fun string(key: String, default: String) = map[key] as? String ?: default
    override fun edit(block: KeyValue.Editor.() -> Unit) {
        object : KeyValue.Editor {
            override fun int(key: String, v: Int) { map[key] = v }
            override fun float(key: String, v: Float) { map[key] = v }
            override fun bool(key: String, v: Boolean) { map[key] = v }
            override fun string(key: String, v: String) { map[key] = v }
            override fun remove(key: String) { map.remove(key) }
        }.block()
    }
}

class SettingsTest {
    @Test
    fun displayRoundTripAndMigration() {
        val kv = MemoryKv()
        assertEquals(DisplaySettings(), DisplaySettings.load(kv)) // nothing saved: the desktop's defaults
        kv.map["scale"] = 3 // before fractional scales
        kv.map["dx"] = -10
        assertEquals(DisplaySettings(dx = -10, scale = 3.0), DisplaySettings.load(kv))
        val s = DisplaySettings(-10, -8, 1.5, 15, -20, false)
        s.save(kv)
        assertEquals(s, DisplaySettings.load(kv))
        assertNull(kv.map["scale"]) // the old key is gone
        assertEquals(DisplayOptions(showAbove = 15, showBelow = -20, alwaysShowNotifications = false), s.displayOptions())
    }

    @Test
    fun invalidDisplayValuesNeverLoadOrSave() {
        val kv = MemoryKv()
        kv.map["dx"] = 500 // e.g. written by an older build
        assertEquals(DisplaySettings(), DisplaySettings.load(kv))
        assertThrows(IllegalArgumentException::class.java) { DisplaySettings(dx = 241).save(kv) }
        assertThrows(IllegalArgumentException::class.java) { DisplaySettings(scale = Double.NaN).save(kv) }
        assertEquals(500, kv.map["dx"]) // the failed saves wrote nothing
    }

    @Test
    fun appSettings() {
        val kv = MemoryKv()
        assertEquals(AppSettings(), AppSettings.load(kv))
        val a = AppSettings(project = "my-site", refreshSeconds = 60, rotateSeconds = 0, ai = false, owner = OwnerMode.KEEP_ON_PHONE)
        a.save(kv)
        assertEquals(a, AppSettings.load(kv))
        assertEquals("my-site", a.apiConfig().project)
        for (bad in listOf(
            AppSettings(api = "http://api.example.com"), AppSettings(api = "https://user:pw@api.example.com"),
            AppSettings(api = "https://api.example.com/?q=1"), AppSettings(api = "not a url"), AppSettings(project = "Bad Slug"),
            AppSettings(refreshSeconds = 29), AppSettings(rotateSeconds = -1), AppSettings(rotateSeconds = 3601),
        )) {
            assertNotNull(bad.toString(), bad.problem())
            assertThrows(IllegalArgumentException::class.java) { bad.save(MemoryKv()) }
        }
        kv.map["owner"] = "SOMETHING_ELSE"
        assertEquals(OwnerMode.PREFER_LAPTOP, AppSettings.load(kv).owner)
        kv.map["refresh_s"] = 5 // invalid: everything falls back to the defaults, never half-valid
        assertEquals(AppSettings(), AppSettings.load(kv))
    }

    @Test
    fun calibrationNudgesAsTheDesktopKeys() {
        val s = DisplaySettings(dx = 0, dy = 0)
        assertEquals(-1, nudge(s, Nudge.UP, 1).dy)
        assertEquals(10, nudge(s, Nudge.DOWN, 10).dy)
        assertEquals(-1, nudge(s, Nudge.RIGHT, 1).dx)
        assertEquals(10, nudge(s, Nudge.LEFT, 10).dx)
        assertEquals(240, nudge(DisplaySettings(dx = 235), Nudge.LEFT, 10).dx) // clamped: never out of range
        assertEquals(-120, nudge(DisplaySettings(dy = -115), Nudge.UP, 10).dy)
        assertNull(nudge(DisplaySettings(dx = 240, dy = 120), Nudge.DOWN, 10).problem())
    }

    @Test
    fun ownershipNeverReadyBeforeActive() {
        for (p in Phase.entries) {
            val v = ownershipView(p, requested = true)
            assertEquals(p.name, p == Phase.ACTIVE, v.progress == Progress.READY)
        }
        assertEquals(OwnershipView(Progress.REQUESTED, Holder.NOBODY), ownershipView(Phase.STOPPED, true))
        assertEquals(OwnershipView(Progress.IDLE, Holder.NOBODY), ownershipView(Phase.STOPPED, false))
        assertEquals(OwnershipView(Progress.PENDING, Holder.OTHER_DEVICE), ownershipView(Phase.STANDBY, false))
        assertEquals(Progress.PENDING, ownershipView(Phase.CONNECTING, false).progress)
        assertEquals(Progress.FAILED, ownershipView(Phase.NEEDS_PERMISSION, false).progress)
        assertEquals(Holder.THIS_PHONE, ownershipView(Phase.ACTIVE, false).holder)
    }
}
