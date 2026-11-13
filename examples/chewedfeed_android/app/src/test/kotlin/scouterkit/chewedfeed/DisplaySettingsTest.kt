package scouterkit.chewedfeed

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import scouterkit.chewedfeed.dashboard.DisplayOptions

class DisplaySettingsTest {
    @Test
    fun ranges() {
        assertNull(DisplaySettings(-10, -8, 1.5).problem())
        assertNull(DisplaySettings(-240, 120, 0.1).problem())
        assertNull(DisplaySettings(240, -120, 3.0).problem())
        assertEquals("dx must be between -240 and 240", DisplaySettings(241, 0, 2.0).problem())
        assertEquals("dy must be between -120 and 120", DisplaySettings(0, -121, 2.0).problem())
        assertNull(DisplaySettings(0, 0, 2.0, 90, -90).problem())
        for ((up, down) in listOf(91 to -30, 9 to -91)) assertEquals("tilt angles must be between -90 and 90", DisplaySettings(0, 0, 2.0, up, down).problem())
        assertEquals(DisplayOptions(showAbove = 15, showBelow = -20), DisplaySettings(showAbove = 15, showBelow = -20).displayOptions())
        assertEquals(9 to -30, DisplaySettings().let { it.showAbove to it.showBelow }) // the desktop's defaults
        for (bad in listOf(3.01, 0.09, Double.NaN)) assertEquals("text scale must be between 0.1 and 3", DisplaySettings(0, 0, bad).problem())
    }
}
