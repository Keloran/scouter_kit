package scouterkit.chewedfeed.settings

import scouterkit.chewedfeed.api.ApiConfig

// Who should hold the glasses when both the laptop and this phone could: the laptop while it is awake (the
// default, handing over automatically), or this phone until changed.
enum class OwnerMode { PREFER_LAPTOP, KEEP_ON_PHONE }

// The Chewedfeed side of this phone's setup (desktop flags -api, -project, -refresh, -rotate, -details), the data
// sources shown, and the owner mode. The API address and project are checked by ApiConfig (the API client's own
// rules). The session token is not here: the API's SessionCredentialStore keeps it encrypted.
data class AppSettings(
    val api: String = DEFAULT_API,
    val project: String = "",           // only this project slug; "" all
    val refreshSeconds: Int = 300,
    val rotateSeconds: Int = 12,        // 0: no automatic rotation
    val details: Boolean = true,        // per-project uptime and tickets
    val sites: Boolean = true,
    val ai: Boolean = true,
    val stats: Boolean = true,
    val owner: OwnerMode = OwnerMode.PREFER_LAPTOP,
) {
    // null when valid, else what to fix.
    fun problem(): String? = when {
        apiProblem() != null -> apiProblem()
        refreshSeconds !in 30..86_400 -> "refresh must be between 30 seconds and a day"
        rotateSeconds != 0 && rotateSeconds !in 1..3_600 -> "rotation must be 0 (off) or 1 to 3600 seconds"
        else -> null
    }

    private fun apiProblem() = runCatching { apiConfig() }.exceptionOrNull()?.let { it.message ?: "invalid API settings" }

    // For the API client (CF-43).
    fun apiConfig(): ApiConfig = ApiConfig.create(api, project, details)

    fun save(kv: KeyValue) {
        require(problem() == null) { problem()!! }
        kv.edit {
            string("api", api); string("project", project)
            int("refresh_s", refreshSeconds); int("rotate_s", rotateSeconds)
            bool("details", details); bool("sites", sites); bool("ai", ai); bool("stats", stats)
            string("owner", owner.name)
        }
    }

    companion object {
        const val DEFAULT_API = "https://api.chewedfeed.com"
        // What was saved, or the defaults for anything invalid (never handed on unvalidated).
        fun load(kv: KeyValue): AppSettings {
            val d = AppSettings()
            val s = AppSettings(
                kv.string("api", d.api), kv.string("project", d.project),
                kv.int("refresh_s", d.refreshSeconds), kv.int("rotate_s", d.rotateSeconds),
                kv.bool("details", d.details), kv.bool("sites", d.sites), kv.bool("ai", d.ai), kv.bool("stats", d.stats),
                OwnerMode.entries.firstOrNull { it.name == kv.string("owner", "") } ?: d.owner,
            )
            return if (s.problem() == null) s else d
        }
    }
}
