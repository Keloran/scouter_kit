package scouterkit.chewedfeed

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class DisplaySettingsTest {
    @Test
    fun ranges() {
        assertNull(DisplaySettings(-10, -8, 1.5).problem())
        assertNull(DisplaySettings(-240, 120, 0.1).problem())
        assertNull(DisplaySettings(240, -120, 3.0).problem())
        assertEquals("dx must be between -240 and 240", DisplaySettings(241, 0, 2.0).problem())
        assertEquals("dy must be between -120 and 120", DisplaySettings(0, -121, 2.0).problem())
        for (bad in listOf(3.01, 0.09, Double.NaN)) assertEquals("text scale must be between 0.1 and 3", DisplaySettings(0, 0, bad).problem())
    }
}
