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
import scouterkit.chewedfeed.settings.AppSettings
import scouterkit.chewedfeed.service.GlassesService
import scouterkit.chewedfeed.api.SessionToken
import scouterkit.chewedfeed.api.SitesCache
import scouterkit.chewedfeed.api.androidSessionCredentials
import scouterkit.chewedfeed.settings.Prefs
import scouterkit.chewedfeed.notifications.NotificationSettings
import scouterkit.chewedfeed.notifications.PhoneNotifications

// App settings: this phone's display settings for the glasses (alignment, text scale, tilt), the Chewedfeed API
// and sources, and notifications. Saves only when every value is valid.
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
        val always = CheckBox(this).apply { setText(R.string.always_notes); isChecked = current.alwaysNotes }
        list.addView(always)
        val state = TextView(this)
        // Chewedfeed: the API, the token (encrypted in the Keystore), refresh and rotation, and what to show.
        val kv = Prefs(this, "app")
        val app = AppSettings.load(kv)
        val tokens = androidSessionCredentials(this)
        val saved = runCatching { tokens.load() != null }.getOrDefault(false) // unreadable: as if none
        list.addView(TextView(this).apply { setText(R.string.api_settings) })
        fun text(label: Int, value: String, password: Boolean = false) = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or
                if (password) InputType.TYPE_TEXT_VARIATION_PASSWORD else InputType.TYPE_TEXT_VARIATION_URI
            setText(value)
        }.also {
            list.addView(TextView(this).apply { setText(label) })
            list.addView(it)
        }
        val api = text(R.string.api_label, app.api)
        val project = text(R.string.project_label, app.project)
        val token = text(if (saved) R.string.token_label_set else R.string.token_label_unset, "", password = true)
        list.addView(Button(this).apply {
            setText(R.string.token_clear)
            setOnClickListener { runCatching { tokens.save(null) }; SitesCache.forApp(this@SettingsActivity).clear(); state.setText(R.string.token_cleared) }
        })
        val refresh = field(R.string.refresh_label, String.format(Locale.ROOT, "%d", app.refreshSeconds))
        val rotate = field(R.string.rotate_label, String.format(Locale.ROOT, "%d", app.rotateSeconds))
        fun check(label: Int, on: Boolean) = CheckBox(this).apply { setText(label); isChecked = on }.also { list.addView(it) }
        val sites = check(R.string.source_sites, app.sites)
        val details = check(R.string.source_details, app.details)
        val ai = check(R.string.source_ai, app.ai)
        val stats = check(R.string.source_stats, app.stats)
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
                    above.text.toString().toIntOrNull() ?: Int.MIN_VALUE, below.text.toString().toIntOrNull() ?: Int.MIN_VALUE, always.isChecked)
                val a = AppSettings.load(kv).copy(api = api.text.toString().trim(), project = project.text.toString().trim(),
                    refreshSeconds = refresh.text.toString().toIntOrNull() ?: -1, rotateSeconds = rotate.text.toString().toIntOrNull() ?: -1,
                    sites = sites.isChecked, details = details.isChecked, ai = ai.isChecked, stats = stats.isChecked)
                val entered = token.text.toString().trim()
                val session = if (entered.isEmpty()) null else runCatching { SessionToken.parse(entered) }
                val problem = s.problem() ?: a.problem() ?: session?.exceptionOrNull()?.message
                if (problem != null) {
                    state.text = problem
                } else {
                    s.save(this@SettingsActivity)
                    a.save(kv)
                    GlassesService.reload(this@SettingsActivity) // a source switched off clears what the laptop shared
                    session?.getOrNull()?.let { tokens.save(it); SitesCache.forApp(this@SettingsActivity).clear(); token.setText("") }
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
