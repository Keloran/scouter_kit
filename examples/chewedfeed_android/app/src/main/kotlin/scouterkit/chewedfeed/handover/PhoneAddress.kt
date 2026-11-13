package scouterkit.chewedfeed.handover

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import java.net.Inet4Address

// Where the laptop reaches this phone: its IPv4 addresses on the current Wi-Fi or Ethernet network (not cellular,
// where the laptop cannot reach it), as host:port for the listener.
object PhoneAddress {
    fun endpoints(context: Context, port: Int = PeerServer.DEFAULT_PORT): List<String> {
        val cm = context.getSystemService(ConnectivityManager::class.java)
        val net = cm.activeNetwork ?: return emptyList()
        val caps = cm.getNetworkCapabilities(net) ?: return emptyList()
        if (!caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) && !caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)) return emptyList()
        return cm.getLinkProperties(net)?.linkAddresses.orEmpty().map { it.address }
            .filter { it is Inet4Address && !it.isLoopbackAddress }.mapNotNull { it.hostAddress?.let { a -> "$a:$port" } }
    }
}
