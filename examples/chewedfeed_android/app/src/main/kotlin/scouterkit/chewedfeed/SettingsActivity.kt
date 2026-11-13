package scouterkit.chewedfeed

import android.app.Activity
import android.os.Bundle
import android.widget.TextView

// Placeholder for setup, calibration and ownership controls.
class SettingsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.settings)
        setContentView(TextView(this).apply { setText(R.string.settings_placeholder) })
    }
}
