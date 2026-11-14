package scouterkit.chewedfeed

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.content.Intent
import android.widget.LinearLayout
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
        val ui = AppUi(this)
        val current = DisplaySettings.load(this)
        val page = ui.screen(R.string.settings, R.string.settings_subtitle)
        val kv = Prefs(this, "app")
        val app = AppSettings.load(kv)
        val tokens = androidSessionCredentials(this)
        val saved = runCatching { tokens.load() != null }.getOrDefault(false)
        val notes = PhoneNotifications.load(this)
        val state = TextView(this).apply {
            accessibilityLiveRegion = android.view.View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        ui.styleStatus(state)
        val feed = ui.card(page, R.string.feed_title, R.string.feed_subtitle)
        val sites = ui.toggle(R.string.source_sites, app.sites).also { feed.addView(it) }
        val details = ui.toggle(R.string.source_details, app.details).also { feed.addView(it) }
        val ai = ui.toggle(R.string.source_ai, app.ai).also { feed.addView(it) }
        val stats = ui.toggle(R.string.source_stats, app.stats).also { feed.addView(it) }
        val account = ui.disclosure(page, R.string.account_title, R.string.account_subtitle)
        val uriType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or InputType.TYPE_TEXT_VARIATION_URI
        val api = ui.field(account, R.string.api_label, app.api, uriType)
        val project = ui.field(account, R.string.project_label, app.project, uriType)
        val token = ui.field(account, if (saved) R.string.token_label_set else R.string.token_label_unset, "",
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD).apply {
            isSaveEnabled = false // Credentials never enter the activity's saved view state.
            importantForAutofill = android.view.View.IMPORTANT_FOR_AUTOFILL_NO
        }
        account.addView(ui.button(R.string.token_clear) {
            runCatching { tokens.save(null) }.onSuccess {
                SitesCache.forApp(this).clear()
                token.setText("")
                state.setText(R.string.token_cleared)
            }.onFailure { state.text = it.message }
        })
        val timing = ui.disclosure(page, R.string.timing_title, R.string.timing_subtitle)
        val number = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_SIGNED
        val refresh = ui.field(timing, R.string.refresh_label, app.refreshSeconds.toString(), number)
        val rotate = ui.field(timing, R.string.rotate_label, app.rotateSeconds.toString(), number)
        val display = ui.disclosure(page, R.string.display_title, R.string.display_subtitle)
        display.addView(ui.text(R.string.display_settings))
        val scale = ui.field(display, R.string.scale_label,
            String.format(Locale.ROOT, "%.2f", current.scale).trimEnd('0').trimEnd('.'),
            InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_DECIMAL)
        val dx = ui.field(display, R.string.dx_label, current.dx.toString(), number)
        val dy = ui.field(display, R.string.dy_label, current.dy.toString(), number)
        display.addView(ui.text(R.string.tilt_settings))
        val above = ui.field(display, R.string.show_above_label, current.showAbove.toString(), number)
        val below = ui.field(display, R.string.show_below_label, current.showBelow.toString(), number)
        val always = ui.toggle(R.string.always_notes, current.alwaysNotes).also { display.addView(it) }
        val notifications = ui.disclosure(page, R.string.notifications_title, R.string.notifications_subtitle)
        notifications.addView(ui.text(R.string.notifications_settings))
        ui.styleStatus(access)
        notifications.addView(access)
        notifications.addView(ui.button(R.string.notification_access) {
            startActivity(Intent(android.provider.Settings.ACTION_NOTIFICATION_LISTENER_DETAIL_SETTINGS)
                .putExtra(android.provider.Settings.EXTRA_NOTIFICATION_LISTENER_COMPONENT_NAME,
                    PhoneNotifications.component(this).flattenToString()))
        })
        val mirror = ui.toggle(R.string.mirror_notifications, notes.enabled).also { notifications.addView(it) }
        val excluded = ui.field(notifications, R.string.excluded_apps, notes.excluded.sorted().joinToString(", "),
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS)
        val footer = ui.column().apply {
            setPadding(ui.dp(20), ui.dp(8), ui.dp(20), ui.dp(12))
            setBackgroundColor(ui.color(R.color.surface))
        }
        footer.addView(state)
        footer.addView(ui.button(R.string.save_changes, primary = true) {
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
                val invalid = when {
                    s.dx !in -240..240 -> display to dx
                    s.dy !in -120..120 -> display to dy
                    s.scale.isNaN() || s.scale !in 0.1..3.0 -> display to scale
                    s.showAbove !in -90..90 -> display to above
                    s.showBelow !in -90..90 -> display to below
                    a.refreshSeconds !in 30..86400 -> timing to refresh
                    a.rotateSeconds != 0 && a.rotateSeconds !in 1..3600 -> timing to rotate
                    session?.isFailure == true -> account to token
                    else -> account to api
                }
                if (invalid.first.visibility != android.view.View.VISIBLE) {
                    ((invalid.first.parent as LinearLayout).getChildAt(1) as android.widget.Button).performClick()
                }
                invalid.second.error = problem
                invalid.second.requestFocus()
            } else {
                s.save(this@SettingsActivity)
                a.save(kv)
                GlassesService.reload(this@SettingsActivity) // a source switched off clears what the laptop shared
                session?.getOrNull()?.let { tokens.save(it); SitesCache.forApp(this@SettingsActivity).clear(); token.setText("") }
                PhoneNotifications.save(this@SettingsActivity,
                    NotificationSettings(mirror.isChecked, PhoneNotifications.parseExcluded(excluded.text.toString())))
                state.setText(R.string.saved)
            }
        })
        val root = ui.column()
        root.addView(ui.scroll(page), LinearLayout.LayoutParams(-1, 0, 1f))
        root.addView(footer)
        setContentView(root)
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
