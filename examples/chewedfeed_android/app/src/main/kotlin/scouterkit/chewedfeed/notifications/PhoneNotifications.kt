package scouterkit.chewedfeed.notifications

import android.app.Notification
import android.app.NotificationManager
import android.content.ComponentName
import android.content.Context
import android.content.pm.PackageManager
import android.os.SystemClock
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification

// The phone's notifications, with the access the user grants in Android's settings (optional: everything else
// works without it). Contents go only to Hub, in memory; nothing is logged or stored.
class PhoneNotifications : NotificationListenerService() {
    override fun onCreate() {
        super.onCreate()
        Hub.configure(Feed(packageName, { load(this) }, SystemClock::elapsedRealtime))
    }

    override fun onListenerConnected() = Hub.listenerConnected(true)
    override fun onListenerDisconnected() = Hub.listenerConnected(false)

    override fun onNotificationPosted(sbn: StatusBarNotification) {
        val n = sbn.notification ?: return
        val x = n.extras
        Hub.posted(Posted(
            key = sbn.key, packageName = sbn.packageName, app = label(sbn.packageName),
            title = x.getCharSequence(Notification.EXTRA_TITLE), text = x.getCharSequence(Notification.EXTRA_TEXT),
            bigText = x.getCharSequence(Notification.EXTRA_BIG_TEXT),
            ongoing = sbn.isOngoing, groupSummary = n.flags and Notification.FLAG_GROUP_SUMMARY != 0,
            foregroundService = n.flags and Notification.FLAG_FOREGROUND_SERVICE != 0,
            secret = n.visibility == Notification.VISIBILITY_SECRET,
        ))
    }

    override fun onNotificationRemoved(sbn: StatusBarNotification) = Hub.removed(sbn.key)

    // The app's name; its package name when this app may not see the package (Android 11+ package visibility).
    private fun label(pkg: String) = try {
        packageManager.getApplicationLabel(packageManager.getApplicationInfo(pkg, 0)).toString()
    } catch (e: PackageManager.NameNotFoundException) {
        pkg
    }

    companion object {
        private const val PREFS = "notifications"

        fun load(c: Context): NotificationSettings {
            val p = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            return NotificationSettings(p.getBoolean("enabled", true), p.getStringSet("excluded", emptySet())!!.toSet())
        }

        fun save(c: Context, s: NotificationSettings) {
            c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
                .putBoolean("enabled", s.enabled).putStringSet("excluded", s.excluded).apply()
        }

        fun component(c: Context) = ComponentName(c, PhoneNotifications::class.java)

        fun accessGranted(c: Context) = c.getSystemService(NotificationManager::class.java).isNotificationListenerAccessGranted(component(c))

        // "com.a, com.b" as a set of package names.
        fun parseExcluded(text: String) = text.split(',', ' ', '\n').map { it.trim() }.filter { it.isNotEmpty() }.toSet()
    }
}
