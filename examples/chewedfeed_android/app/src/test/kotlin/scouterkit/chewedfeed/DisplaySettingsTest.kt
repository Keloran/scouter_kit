package scouterkit.chewedfeed

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class DisplaySettingsTest {
    @Test
    fun ranges() {
        assertNull(DisplaySettings(-10, -8, 2).problem())
        assertNull(DisplaySettings(-240, 120, 1).problem())
        assertEquals("dx must be between -240 and 240", DisplaySettings(241, 0, 2).problem())
        assertEquals("dy must be between -120 and 120", DisplaySettings(0, -121, 2).problem())
        assertEquals("text scale must be 1 to 4", DisplaySettings(0, 0, 5).problem())
    }
}
