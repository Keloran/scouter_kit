package scouterkit.chewedfeed.handover

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo

// The phone's LAN advertisement for the laptop (CF-53): DNS-SD SERVICE_TYPE on the peer port, TXT v=1, through the
// platform's mDNS responder (no permission needed; it follows Wi-Fi changes itself). It names no device, address,
// key or user: the instance is "chewedfeed" (the platform renames it on a clash), and the laptop treats any answer as
// a hint that the pinned TLS keys must confirm (Go handover.DiscoveryRoute).
object LanAdvert {
    const val SERVICE_TYPE = "_chewedfeed._tcp"
    const val VERSION = "1"

    fun start(context: Context, port: Int): AutoCloseable {
        val nsd = context.getSystemService(NsdManager::class.java) ?: return AutoCloseable {}
        val info = NsdServiceInfo().apply {
            serviceName = "chewedfeed"
            serviceType = SERVICE_TYPE
            this.port = port
            setAttribute("v", VERSION)
        }
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) {} // the configured endpoint still works
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) {}
        }
        runCatching { nsd.registerService(info, NsdManager.PROTOCOL_DNS_SD, listener) }.onFailure { return AutoCloseable {} }
        return AutoCloseable { runCatching { nsd.unregisterService(listener) } }
    }
}
