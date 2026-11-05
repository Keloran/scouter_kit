package scouterkit.ssh

import com.jcraft.jsch.ChannelExec
import com.jcraft.jsch.HostKey
import com.jcraft.jsch.HostKeyRepository
import com.jcraft.jsch.JSch
import com.jcraft.jsch.JSchException
import com.jcraft.jsch.UserInfo
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64

// Starts a mosh-server over SSH as mosh.pl does and returns where it listens and its key (R18). The key
// goes straight to the mosh client; SSH is closed right after (the server waits 60 s for the first datagram).
class Bootstrap(private val vault: Vault) {
    data class Mosh(val ip: String, val port: Int, val key: String, val pid: Int)

    class UnknownHost(val fingerprint: String) : Exception("unknown host key $fingerprint")

    fun start(host: String, port: Int, user: String, command: String, ports: String, previousPid: Int): Mosh {
        val identity = vault.get(Vault.SSH_KEY) ?: throw IllegalStateException("no SSH key: import one in Settings")
        val ip = InetAddress.getByName(host).hostAddress!!
        val jsch = JSch()
        jsch.addIdentity("key", identity, null, vault.get(Vault.SSH_PASSPHRASE))
        val hosts = Hosts("$host:$port", vault.text(Vault.HOST_KEY))
        jsch.hostKeyRepository = hosts
        val session = jsch.getSession(user, ip, port)
        session.setConfig("StrictHostKeyChecking", "yes")
        session.userInfo = Quiet
        try {
            session.connect(15000)
        } catch (e: JSchException) {
            hosts.unknown?.let { throw UnknownHost(it) }
            throw e
        }
        val watchdog = Thread {
            try {
                Thread.sleep(30000)
                session.disconnect()
            } catch (_: InterruptedException) {
            }
        }
        watchdog.start()
        try {
            val channel = session.openChannel("exec") as ChannelExec
            channel.setPty(true)
            channel.setCommand("sh -c '" + remote(command, ports, previousPid) + "'")
            val reader = channel.inputStream.bufferedReader()
            channel.connect(15000)
            val connect = Regex("^MOSH CONNECT (\\d+?) ([A-Za-z0-9/+]{22})\\s*$")
            val detached = Regex("mosh-server detached, pid = (\\d+)")
            val output = StringBuilder()
            var found: MatchResult? = null
            var pid = 0
            while (found == null || pid == 0) {
                val line = reader.readLine()?.trimEnd('\r') ?: break
                detached.find(line)?.let { pid = it.groupValues[1].toInt() }
                val m = connect.find(line)
                if (m != null) found = m else if (found == null) output.appendLine(line)
            }
            channel.disconnect()
            val m = found ?: throw IllegalStateException("mosh-server did not start: " + output.toString().take(500))
            return Mosh(ip, m.groupValues[1].toInt(), m.groupValues[2], pid)
        } finally {
            watchdog.interrupt()
            session.disconnect()
        }
    }

    // POSIX sh inside single quotes, so the login shell may be any (fish, zsh, ...).
    private fun remote(command: String, ports: String, previousPid: Int): String {
        val kill = if (previousPid > 0)
            "[ \"\$(cat /proc/$previousPid/comm 2>/dev/null)\" = mosh-server ] && kill $previousPid; " else ""
        val cmd = if (command.isBlank()) "" else " -- " + command.replace("'", "")
        val range = if (Regex("\\d{1,5}(:\\d{1,5})?").matches(ports)) " -p $ports" else ""
        return kill + "exec mosh-server new -c 256 -s -l LANG=C.UTF-8$range$cmd"
    }

    // Trust on first use: the fingerprint is shown and stored once the user confirms it (UnknownHost).
    private class Hosts(val name: String, val stored: String?) : HostKeyRepository {
        var unknown: String? = null

        override fun check(host: String?, key: ByteArray): Int {
            val fingerprint = fingerprint(key)
            if (stored == "$name $fingerprint") return HostKeyRepository.OK
            if (stored != null && stored.startsWith("$name ")) return HostKeyRepository.CHANGED
            unknown = fingerprint
            return HostKeyRepository.NOT_INCLUDED
        }

        override fun add(hostkey: HostKey?, ui: UserInfo?) {}
        override fun remove(host: String?, type: String?) {}
        override fun remove(host: String?, type: String?, key: ByteArray?) {}
        override fun getKnownHostsRepositoryID() = "scouter"
        override fun getHostKey(): Array<HostKey> = emptyArray()
        override fun getHostKey(host: String?, type: String?): Array<HostKey> = emptyArray()
    }

    private object Quiet : UserInfo {
        override fun getPassphrase(): String? = null
        override fun getPassword(): String? = null
        override fun promptPassword(message: String?) = false
        override fun promptPassphrase(message: String?) = false
        override fun promptYesNo(message: String?) = false
        override fun showMessage(message: String?) {}
    }

    companion object {
        fun fingerprint(key: ByteArray): String =
            "SHA256:" + Base64.getEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(key))
    }
}
