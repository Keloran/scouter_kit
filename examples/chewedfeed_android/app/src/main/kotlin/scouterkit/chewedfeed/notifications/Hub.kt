package scouterkit.chewedfeed.notifications

import java.util.concurrent.Executor

// Between the listener service and whoever shows notifications (the dashboard runtime). Nothing is stored here:
// with no one attached, notifications are dropped. Never sent to another host (policy: notification history is
// local).
object Hub {
    private var feed: Feed? = null
    private var sink: Pair<Executor, (FeedEvent) -> Unit>? = null

    @Volatile var connected = false // notification access granted and the listener bound
        private set

    @Synchronized fun configure(feed: Feed) {
        this.feed = feed
    }

    // One receiver at a time; events arrive on executor.
    @Synchronized fun attach(executor: Executor, receive: (FeedEvent) -> Unit) {
        sink = executor to receive
    }

    @Synchronized fun detach() {
        sink = null
        feed?.reset()
    }

    @Synchronized fun listenerConnected(on: Boolean) {
        connected = on
        feed?.reset()
    }

    fun posted(p: Posted) = deliver { it.posted(p) }
    fun removed(key: String) = deliver { it.removed(key) }

    // Decided under the lock, delivered outside it. A receiver that detached meanwhile may get one last event.
    private fun deliver(decide: (Feed) -> FeedEvent?) {
        val target: Pair<Executor, (FeedEvent) -> Unit>
        val event: FeedEvent
        synchronized(this) {
            target = sink ?: return
            event = feed?.let(decide) ?: return
        }
        target.first.execute { target.second(event) }
    }
}
