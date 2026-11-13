package scouterkit.chewedfeed.providers

import android.content.ComponentName
import android.content.Context
import android.media.MediaMetadata
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import android.os.Handler
import scouterkit.chewedfeed.notifications.Feed

// One media session as far as the footer cares.
data class Player(val id: String, val playing: Boolean, val title: String?)

// The footer's music title (go/examples/chewedfeed music.go currentMusicTitle): the first playing player's title,
// cleaned like other text for the glasses; "" (the clock) when nothing plays or nothing has a title.
fun nowPlaying(players: List<Player>): String =
    players.asSequence().filter { it.playing }.map { Feed.clean(it.title ?: "").replace('\n', ' ') }.firstOrNull { it.isNotEmpty() } ?: ""

// Keeps one subscription per current session and the title they add up to: a new session list drops every old
// subscription first; stop() drops them all and reports "" (the clock).
class Watcher<C>(
    private val subscribe: (C, () -> Unit) -> () -> Unit, // returns the unsubscribe
    private val read: (C) -> Player,
    private val onTitle: (String) -> Unit,
) {
    private val current = mutableListOf<Pair<C, () -> Unit>>()
    private var title = ""
    val subscribed get() = current.size

    fun sessions(list: List<C>) {
        unsubscribe()
        for (c in list) current += c to subscribe(c) { update() }
        update()
    }

    fun stop() {
        unsubscribe()
        publish("")
    }

    fun update() = publish(nowPlaying(current.map { read(it.first) }))

    private fun unsubscribe() {
        for ((_, off) in current) runCatching { off() }
        current.clear()
    }

    private fun publish(t: String) {
        if (t == title) return
        title = t
        onTitle(t)
    }
}

// The phone's media sessions, read with the notification access the user granted to PhoneNotifications (Android
// requires an enabled notification listener for MediaSessionManager.getActiveSessions; no playback control).
// Without that access, or with nothing playing, the title is "" and the footer shows the clock. Callbacks run on
// handler.
class PhoneMedia(context: Context, private val listener: ComponentName, private val handler: Handler, onTitle: (String) -> Unit) {
    private val manager = context.getSystemService(MediaSessionManager::class.java)
    private var started = false
    private val watcher = Watcher<MediaController>(
        subscribe = { c, changed ->
            val cb = object : MediaController.Callback() {
                override fun onPlaybackStateChanged(state: PlaybackState?) = changed()
                override fun onMetadataChanged(metadata: MediaMetadata?) = changed()
                override fun onSessionDestroyed() = changed()
            }
            c.registerCallback(cb, handler)
            ({ c.unregisterCallback(cb) })
        },
        read = { c ->
            Player(c.packageName, c.playbackState?.state == PlaybackState.STATE_PLAYING, c.metadata?.getString(MediaMetadata.METADATA_KEY_TITLE))
        },
        onTitle = onTitle,
    )
    private val sessions = MediaSessionManager.OnActiveSessionsChangedListener { list -> if (started) watcher.sessions(list.orEmpty()) }

    fun start() {
        if (started) return
        started = true
        val list = try {
            manager.addOnActiveSessionsChangedListener(sessions, listener, handler)
            manager.getActiveSessions(listener)
        } catch (e: SecurityException) { // no notification access (or it was revoked): the clock
            emptyList()
        }
        watcher.sessions(list)
    }

    fun stop() {
        if (!started) return
        started = false
        runCatching { manager.removeOnActiveSessionsChangedListener(sessions) }
        watcher.stop()
    }
}
