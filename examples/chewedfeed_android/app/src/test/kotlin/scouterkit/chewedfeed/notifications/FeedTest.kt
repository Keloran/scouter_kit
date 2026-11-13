package scouterkit.chewedfeed.notifications

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.dashboard.Notification

// Synthetic notifications only.
class FeedTest {
    private var now = 0L
    private var settings = NotificationSettings()
    private val feed = Feed("com.chewedfeed.glasses", { settings }, { now })

    private fun posted(key: String = "k", pkg: String = "com.example.mail", title: String? = "Hi", text: String? = "Body", big: String? = null) =
        Posted(key, pkg, "Mail", title, text, big)

    @Test
    fun showsCleanedText() {
        val e = feed.posted(posted(title = "<b>Bob</b>\u0000 &amp; Alice \uD83D\uDC68\u200D\uD83D\uDC69", text = "line one<br>line&nbsp;two</p>three &#x1F680; &bogus;\u001b[31m"))
        assertEquals(FeedEvent.Show("k", Notification("Mail", "Bob & Alice \uD83D\uDC68\u200D\uD83D\uDC69", "line one\nline two\nthree 🚀 &bogus; [31m")), e)
        assertEquals("long", (feed.posted(posted("k2", text = "short", big = "long")) as FeedEvent.Show).note.body) // big text wins
        assertNull(feed.posted(posted("k3", title = " ", text = "<i></i>"))) // nothing to show
    }

    @Test
    fun filtersStatusSelfSecretAndSettings() {
        assertNull(feed.posted(posted(pkg = "com.chewedfeed.glasses"))) // our own service notification
        assertNull(feed.posted(posted().copy(ongoing = true)))
        assertNull(feed.posted(posted().copy(groupSummary = true)))
        assertNull(feed.posted(posted().copy(foregroundService = true)))
        assertNull(feed.posted(posted().copy(secret = true)))
        settings = NotificationSettings(excluded = setOf("com.example.mail"))
        assertNull(feed.posted(posted()))
        settings = NotificationSettings(enabled = false)
        assertNull(feed.posted(posted(pkg = "com.example.chat")))
        settings = NotificationSettings()
        assertTrue(feed.posted(posted()) is FeedEvent.Show)
    }

    @Test
    fun replacementsAndRemovals() {
        assertTrue(feed.posted(posted()) is FeedEvent.Show)
        assertNull(feed.posted(posted())) // re-posted unchanged
        assertEquals("Updated", (feed.posted(posted(text = "Updated")) as FeedEvent.Show).note.body) // same key, new text
        assertEquals(FeedEvent.Dismiss("k"), feed.removed("k"))
        assertNull(feed.removed("k"))
        assertNull(feed.removed("never-shown"))
    }

    @Test
    fun burstsAndMemoryAreBounded() {
        val shown = (0 until 50).count { feed.posted(posted("b$it")) != null }
        assertEquals(10, shown)
        now += 10_000
        assertTrue(feed.posted(posted("later")) is FeedEvent.Show)
        val huge = "x".repeat(100_000) + "🚀".repeat(10_000)
        now += 10_000
        val note = (feed.posted(posted("huge", title = huge, text = huge)) as FeedEvent.Show).note
        assertEquals(Feed.MAX_TEXT + 3, note.summary.length)
        // At most MAX_KEYS remembered: the oldest key's dismissal no longer matches anything.
        for (i in 0 until Feed.MAX_KEYS) { now += 10_000; feed.posted(posted("m$i")) }
        assertNull(feed.removed("huge"))
    }

    @Test
    fun resetForgetsEverything() {
        feed.posted(posted())
        feed.reset()
        assertNull(feed.removed("k"))
        assertTrue(feed.posted(posted()) is FeedEvent.Show) // shown again after access came back
    }
}

class HubTest {
    @Test
    fun deliversOnlyWhileAttached() {
        val got = mutableListOf<FeedEvent>()
        Hub.configure(Feed("self", { NotificationSettings() }, { 0L }))
        Hub.posted(Posted("a", "com.example", "App", "Dropped", null)) // no one attached: dropped, not kept
        Hub.attach({ it.run() }) { got += it }
        Hub.posted(Posted("b", "com.example", "App", "Shown", null))
        Hub.removed("a") // never shown
        Hub.removed("b")
        Hub.listenerConnected(false) // access revoked
        assertTrue(!Hub.connected)
        Hub.detach()
        Hub.posted(Posted("c", "com.example", "App", "After detach", null))
        assertEquals(listOf(FeedEvent.Show("b", Notification("App", "Shown", "")), FeedEvent.Dismiss("b")), got)
    }
}
