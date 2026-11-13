package scouterkit.chewedfeed

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.content.Intent
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import java.util.Locale
import scouterkit.chewedfeed.notifications.NotificationSettings
import scouterkit.chewedfeed.notifications.PhoneNotifications

// App settings. For now this phone's display settings for the glasses; setup, calibration and ownership
// controls come with CF-45.
class SettingsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.settings)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        val current = DisplaySettings.load(this)
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(32, 32, 32, 32) }
        fun field(label: Int, value: String, decimal: Boolean = false) = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER or if (decimal) InputType.TYPE_NUMBER_FLAG_DECIMAL else InputType.TYPE_NUMBER_FLAG_SIGNED
            setText(value) // ASCII digits: parsed back with toIntOrNull / toDoubleOrNull
        }.also {
            list.addView(TextView(this).apply { setText(label) })
            list.addView(it)
        }
        list.addView(TextView(this).apply { setText(R.string.display_settings) })
        val dx = field(R.string.dx_label, String.format(Locale.ROOT, "%d", current.dx))
        val dy = field(R.string.dy_label, String.format(Locale.ROOT, "%d", current.dy))
        val scale = field(R.string.scale_label, String.format(Locale.ROOT, "%.2f", current.scale).trimEnd('0').trimEnd('.'), decimal = true)
        list.addView(TextView(this).apply { setText(R.string.tilt_settings) })
        val above = field(R.string.show_above_label, String.format(Locale.ROOT, "%d", current.showAbove))
        val below = field(R.string.show_below_label, String.format(Locale.ROOT, "%d", current.showBelow))
        val state = TextView(this)
        // Notifications: optional access, an on/off switch and apps left out.
        val notes = PhoneNotifications.load(this)
        list.addView(TextView(this).apply { setText(R.string.notifications_settings) })
        list.addView(access)
        list.addView(Button(this).apply {
            setText(R.string.notification_access)
            setOnClickListener {
                startActivity(Intent(android.provider.Settings.ACTION_NOTIFICATION_LISTENER_DETAIL_SETTINGS)
                    .putExtra(android.provider.Settings.EXTRA_NOTIFICATION_LISTENER_COMPONENT_NAME, PhoneNotifications.component(this@SettingsActivity).flattenToString()))
            }
        })
        val mirror = CheckBox(this).apply { setText(R.string.mirror_notifications); isChecked = notes.enabled }
        list.addView(mirror)
        list.addView(TextView(this).apply { setText(R.string.excluded_apps) })
        val excluded = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            setText(notes.excluded.sorted().joinToString(", "))
        }
        list.addView(excluded)
        list.addView(Button(this).apply {
            setText(R.string.save)
            setOnClickListener {
                val s = DisplaySettings(dx.text.toString().toIntOrNull() ?: Int.MIN_VALUE,
                    dy.text.toString().toIntOrNull() ?: Int.MIN_VALUE, scale.text.toString().replace(',', '.').toDoubleOrNull() ?: Double.NaN,
                    above.text.toString().toIntOrNull() ?: Int.MIN_VALUE, below.text.toString().toIntOrNull() ?: Int.MIN_VALUE)
                val problem = s.problem()
                if (problem != null) {
                    state.text = problem
                } else {
                    s.save(this@SettingsActivity)
                    PhoneNotifications.save(this@SettingsActivity,
                        NotificationSettings(mirror.isChecked, PhoneNotifications.parseExcluded(excluded.text.toString())))
                    state.setText(R.string.saved)
                }
            }
        })
        list.addView(state)
        setContentView(ScrollView(this).apply { addView(list) })
    }

    private val access by lazy { TextView(this) }

    override fun onResume() {
        super.onResume()
        access.setText(if (PhoneNotifications.accessGranted(this)) R.string.notification_access_on else R.string.notification_access_off)
    }

    // The title bar's back arrow: the same as Back.
    override fun onNavigateUp(): Boolean {
        finish()
        return true
    }
}
