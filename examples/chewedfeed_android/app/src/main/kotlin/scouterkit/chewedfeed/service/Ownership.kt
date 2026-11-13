package scouterkit.chewedfeed.service

// What the ownership controls show: one of four states, never READY before the session is up on both arms
// (Phase.ACTIVE follows a native start that needs both), and who holds the glasses as far as this phone knows.
enum class Progress { IDLE, REQUESTED, PENDING, READY, FAILED }
enum class Holder { THIS_PHONE, OTHER_DEVICE, UNKNOWN, NOBODY }

data class OwnershipView(val progress: Progress, val holder: Holder)

// requested: Connect was pressed and the service has not reported since. owner: the handover coordinator's view
// (this, peer, unknown, none); it says who holds the glasses when this phone does not.
fun ownershipView(phase: Phase, requested: Boolean, owner: String = "none"): OwnershipView = when (phase) {
    Phase.ACTIVE -> OwnershipView(Progress.READY, Holder.THIS_PHONE)
    Phase.STANDBY -> OwnershipView(Progress.PENDING, if (owner == "unknown") Holder.UNKNOWN else Holder.OTHER_DEVICE)
    Phase.CONNECTING, Phase.RETRY_WAIT -> OwnershipView(Progress.PENDING, Holder.NOBODY)
    Phase.FAILED, Phase.WAITING_BLUETOOTH, Phase.NEEDS_PERMISSION, Phase.NEEDS_SETUP -> OwnershipView(Progress.FAILED, Holder.NOBODY)
    Phase.STOPPED -> OwnershipView(if (requested) Progress.REQUESTED else Progress.IDLE, Holder.NOBODY)
}
