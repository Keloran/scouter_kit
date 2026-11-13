// Package handover is the ownership reducer of the laptop/phone handover
// contract (tests/handover: controller.schema.json, transitions.json,
// policy.json): Reduce(State, Event) returns the next State and the Effects to
// run, in order. It is pure: no clock, randomness, Bluetooth or network. The
// caller injects a fresh transfer ID in Event.AttemptNonce, measures timers on
// a monotonic clock and reports them as timeout events, and executes effects.
package handover

// Timers and delays from policy.json (milliseconds).
const (
	PeerSuspectMs       = 15000
	LaptopStabilityMs   = 3000
	ReleaseDeadlineMs   = 5000
	AcquireDeadlineMs   = 30000
	PartialArmsMs       = 2000
	PhoneColdStartMs    = 2000
	LaptopColdStartMs   = 0
	RetryInitialMs      = 5000
	RetryMaxMs          = 60000
	PhoneProbeExtraMs   = 2000 // added to the suspect and retry probe delays on the phone
	RetryJitterFraction = 0.2  // up to 20 % added to each retry delay
)

type Role string

const (
	Laptop Role = "laptop"
	Phone  Role = "phone"
)

type Phase string

const (
	Idle       Phase = "idle"
	Requesting Phase = "requesting"
	Yielding   Phase = "yielding"
	Acquiring  Phase = "acquiring"
	Active     Phase = "active"
	Recovering Phase = "recovering"
	Degraded   Phase = "degraded"
)

// Permission is why this host may hold the arms.
type Permission string

const (
	NoPermission Permission = "none"
	Standalone   Permission = "standalone" // not enrolled: no peer to ask
	Released     Permission = "released"   // the peer confirmed its release
	Probe        Permission = "probe"      // bounded, non-preemptive try after the peer went silent
)

type ReleaseIntent string

const (
	NoRelease ReleaseIntent = "none"
	Handover  ReleaseIntent = "handover"
	Rollback  ReleaseIntent = "rollback"
	LocalStop ReleaseIntent = "local_stop"
)

type Arms struct {
	Left  bool `json:"left"`
	Right bool `json:"right"`
}

func (a Arms) both() bool { return a.Left && a.Right }
func (a Arms) any() bool  { return a.Left || a.Right }

// State is controller.schema.json's state. Transfer "" is null.
type State struct {
	Role          Role          `json:"role"`
	Enrolled      bool          `json:"enrolled"`
	Eligible      bool          `json:"eligible"`
	Pinned        bool          `json:"pinned"`
	Phase         Phase         `json:"phase"`
	Arms          Arms          `json:"arms"`
	RendererReady bool          `json:"renderer_ready"`
	Generation    uint64        `json:"generation"`
	Transfer      string        `json:"transfer"`
	PeerReachable bool          `json:"peer_reachable"`
	PeerActive    bool          `json:"peer_active"`
	Permission    Permission    `json:"permission"`
	ReleaseIntent ReleaseIntent `json:"release_intent"`
	PeerEligible  bool          `json:"peer_eligible"`
	PeerPinned    bool          `json:"peer_pinned"`
}

// NewState is a host that has not started.
func NewState(role Role, enrolled bool) State {
	return State{Role: role, Enrolled: enrolled, Phase: Idle, Permission: NoPermission, ReleaseIntent: NoRelease}
}

type EventKind string

const (
	Start           EventKind = "start"            // Eligible
	Availability    EventKind = "availability"     // Eligible
	PeerStatus      EventKind = "peer_status"      // Active, Reachable, Eligible, Pinned
	PreferLocal     EventKind = "prefer_local"     // StableMs: how long this host has been continuously eligible
	ReleaseRequest  EventKind = "release_request"  // Transfer, Reason, Target, PeerPinned
	ReleasedEvent   EventKind = "released"         // Transfer: the peer let the arms go
	ReleaseComplete EventKind = "release_complete" // BothDown, WorkersStopped, ReconnectDisabled
	ArmsChanged     EventKind = "arms_changed"     // Left, Right
	Initialized     EventKind = "initialized"      // RendererReady
	Timeout         EventKind = "timeout"          // Timer, ElapsedMs
	RouteLost       EventKind = "route_lost"
	RouteRestored   EventKind = "route_restored"
	Pin             EventKind = "pin" // Enabled
	ClockAdjusted   EventKind = "clock_adjusted"
	Failed          EventKind = "failed" // Reason
	Stop            EventKind = "stop"
	ReleaseNotice   EventKind = "release_notice" // Transfer: the peer is releasing for us
)

// EventData holds every kind's fields; each kind uses its own (EventKind's comments).
type EventData struct {
	Eligible          bool   `json:"eligible,omitempty"`
	Active            bool   `json:"active,omitempty"`
	Reachable         bool   `json:"reachable,omitempty"`
	Pinned            bool   `json:"pinned,omitempty"`
	StableMs          int64  `json:"stable_ms,omitempty"`
	Transfer          string `json:"transfer,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Target            Role   `json:"target,omitempty"`
	PeerPinned        bool   `json:"peer_pinned,omitempty"`
	BothDown          bool   `json:"both_down,omitempty"`
	WorkersStopped    bool   `json:"workers_stopped,omitempty"`
	ReconnectDisabled bool   `json:"reconnect_disabled,omitempty"`
	Left              bool   `json:"left,omitempty"`
	Right             bool   `json:"right,omitempty"`
	RendererReady     bool   `json:"renderer_ready,omitempty"`
	Timer             Timer  `json:"timer,omitempty"`
	ElapsedMs         int64  `json:"elapsed_ms,omitempty"`
	Enabled           bool   `json:"enabled,omitempty"`
	WallDeltaMs       int64  `json:"wall_delta_ms,omitempty"`
}

// Event is controller.schema.json's event. Generation is the State generation
// the event belongs to: callbacks from an older one are ignored. AttemptNonce
// ("" is null) is a fresh transfer ID the caller supplies in case the event
// starts a new attempt.
type Event struct {
	Kind         EventKind `json:"kind"`
	Generation   uint64    `json:"generation"`
	Data         EventData `json:"data"`
	AttemptNonce string    `json:"attempt_nonce"`
}

type Timer string

const (
	NoTimer         Timer = ""
	StabilityTimer  Timer = "stability"
	ReleaseTimer    Timer = "release"
	AcquireTimer    Timer = "acquire"
	PartialArmTimer Timer = "partial_arms"
	PeerSuspect     Timer = "peer_suspect"
	RetryTimer      Timer = "retry"
	ColdStartTimer  Timer = "cold_start"
)

type EffectKind string

const (
	SendRequest     EffectKind = "send_request"
	SendRelease     EffectKind = "send_release"
	SendReleased    EffectKind = "send_released"
	SendAcquiring   EffectKind = "send_acquiring"
	SendReady       EffectKind = "send_ready"
	SendAbort       EffectKind = "send_abort"
	PublishStatus   EffectKind = "publish_status"
	AcquireBoth     EffectKind = "acquire_both"
	ReleaseBoth     EffectKind = "release_both"
	ForceDisconnect EffectKind = "force_disconnect"
	Redraw          EffectKind = "redraw"
	Reconcile       EffectKind = "reconcile"
	Schedule        EffectKind = "schedule"
	CancelTimers    EffectKind = "cancel_timers"
	Ignore          EffectKind = "ignore"
)

// Effect is controller.schema.json's effect: Generation and Transfer are the
// new State's; Timer and DelayMs only for Schedule.
type Effect struct {
	Kind       EffectKind `json:"kind"`
	Generation uint64     `json:"generation"`
	Transfer   string     `json:"transfer"`
	Timer      Timer      `json:"timer"`
	DelayMs    int64      `json:"delay_ms"`
}

type Transition struct {
	State   State
	Effects []Effect
}

// RetryDelayMs is the delay before retry number failures (0 = the first),
// doubling from RetryInitialMs to RetryMaxMs, plus jitter (0..1, the caller's
// random draw) times RetryJitterFraction. Reset failures on success.
func RetryDelayMs(failures int, jitter float64) int64 {
	d := int64(RetryInitialMs)
	for i := 0; i < failures && d < RetryMaxMs; i++ {
		d *= 2
	}
	d = min(d, RetryMaxMs)
	jitter = min(max(jitter, 0), 1)
	return d + int64(float64(d)*RetryJitterFraction*jitter)
}
