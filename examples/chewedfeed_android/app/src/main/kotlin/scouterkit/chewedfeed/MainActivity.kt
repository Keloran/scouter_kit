package scouterkit.chewedfeed

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView

// Placeholder launcher: shows the native backend and opens the settings.
class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        check(Native.abiVersion() == Native.ABI_VERSION) { "libchewedfeed.so does not match the app" }
        val status = TextView(this).apply {
            text = getString(R.string.status, Native.backend(), Native.abiVersion())
        }
        val settings = Button(this).apply {
            setText(R.string.settings)
            setOnClickListener { startActivity(Intent(this@MainActivity, SettingsActivity::class.java)) }
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(status)
            addView(settings)
        })
    }
}
