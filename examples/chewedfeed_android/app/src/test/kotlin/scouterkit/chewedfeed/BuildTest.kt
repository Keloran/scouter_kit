package scouterkit.chewedfeed

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class BuildTest {
    @Test
    fun applicationIdIsOwnAndPerFlavor() {
        // SSH Scouter is eu.mrogalski.scouter(.emulated): no overlap, so both install side by side.
        assertFalse(BuildConfig.APPLICATION_ID.startsWith("eu.mrogalski.scouter"))
        val expected = mapOf("glasses" to "com.chewedfeed.glasses", "emulated" to "com.chewedfeed.glasses.emulated")
        assertEquals(expected[BuildConfig.FLAVOR], BuildConfig.APPLICATION_ID)
    }

    @Test
    fun abiVersionMatchesNative() {
        // native/boundary.hpp kAbiVersion
        assertEquals(4, Native.ABI_VERSION)
    }
}
