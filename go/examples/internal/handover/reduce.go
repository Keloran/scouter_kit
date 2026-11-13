package handover

// Reduce applies one event. Events of another generation, and events that do
// not apply in the current phase, change nothing and return a single Ignore.
// It never acquires while a reachable peer is active without that peer's
// matching release, never reports released before both arms are down, the
// workers stopped and reconnects disabled, and never lets a timeout or a wall
// clock change take the arms from a healthy owner.
func Reduce(s State, e Event) Transition {
	if e.Generation != s.Generation {
		return ignore(s)
	}
	if !s.Enrolled && peerEvents[e.Kind] {
		return ignore(s) // no peer: nothing it says can be authentic
	}
	d := e.Data
	switch e.Kind {
	case Start:
		if s.Phase != Idle || s.Generation != 0 {
			return ignore(s) // once per process; a restart is a new State
		}
		s.Eligible = d.Eligible
		return start(s, e.AttemptNonce)
	case Availability:
		return availability(s, d.Eligible, e.AttemptNonce)
	case PeerStatus:
		return peerStatus(s, d)
	case PreferLocal:
		return preferLocal(s, d.StableMs, e.AttemptNonce)
	case ReleaseRequest:
		return releaseRequest(s, d)
	case ReleasedEvent:
		if s.Phase != Requesting || d.Transfer != s.Transfer || !s.Eligible {
			return ignore(s)
		}
		s.PeerActive = false
		return acquire(s, s.Transfer, Released)
	case ReleaseNotice:
		if s.Phase != Idle || !s.Eligible || d.Transfer == "" {
			return ignore(s)
		}
		s.Phase, s.Transfer = Requesting, d.Transfer
		s.Generation++
		return done(s, effect(PublishStatus))
	case ReleaseComplete:
		return releaseComplete(s, d)
	case ArmsChanged:
		return armsChanged(s, Arms{Left: d.Left, Right: d.Right})
	case Initialized:
		if s.Phase != Acquiring || !s.Arms.both() || !d.RendererReady {
			return ignore(s)
		}
		s.Phase, s.RendererReady = Active, true
		s.PeerActive = false // both arms are ours: whoever held them before does not any more
		if s.Enrolled {
			return done(s, effect(Redraw), effect(SendReady))
		}
		return done(s, effect(Redraw), effect(PublishStatus))
	case Timeout:
		return timeout(s, d.Timer, d.ElapsedMs, e.AttemptNonce)
	case RouteLost:
		s.PeerReachable = false
		return done(s, effect(PublishStatus), schedule(PeerSuspect, PeerSuspectMs))
	case RouteRestored:
		s.PeerReachable = true
		if s.Phase == Acquiring && s.PeerActive {
			// The previous owner is back mid-probe: only its confirmed release may hand over.
			return rollback(s)
		}
		return done(s, effect(Reconcile))
	case Pin:
		return pin(s, d.Enabled, e.AttemptNonce)
	case Failed:
		if s.Phase == Acquiring || s.Phase == Active {
			return rollback(s)
		}
		return ignore(s)
	case Stop:
		s.Eligible = false
		switch s.Phase {
		case Acquiring:
			return rollback(s)
		case Active:
			return yield(s, s.Transfer, LocalStop, s.Enrolled)
		}
		return done(s, effect(PublishStatus))
	}
	return ignore(s) // ClockAdjusted: deadlines are monotonic; unknown kinds
}

var peerEvents = map[EventKind]bool{PeerStatus: true, PreferLocal: true, ReleaseRequest: true, ReleasedEvent: true,
	ReleaseNotice: true, RouteLost: true, RouteRestored: true}

func start(s State, nonce string) Transition {
	if !s.Eligible {
		return done(s, effect(PublishStatus))
	}
	if !s.Enrolled {
		if nonce == "" {
			return ignore(s)
		}
		return acquire(s, nonce, Standalone)
	}
	// Learn the peer's state before touching the arms; probe only after the cold-start delay.
	s.Phase = Recovering
	return done(s, effect(Reconcile), schedule(ColdStartTimer, coldStartMs(s.Role)))
}

func availability(s State, eligible bool, nonce string) Transition {
	was := s.Eligible
	s.Eligible = eligible
	switch {
	case !eligible && (s.Phase == Active || s.Phase == Acquiring):
		// Lid closed, suspend or app unavailable: release even when pinned or not sleeping.
		if s.Phase == Acquiring {
			return rollback(s)
		}
		if nonce == "" {
			nonce = s.Transfer
		}
		// The laptop tells the phone it is going (release_notice on the wire); the phone just goes.
		return yield(s, nonce, Handover, s.Enrolled && s.Role == Laptop)
	case !eligible && s.Phase == Requesting:
		// No longer able to take the arms: withdraw the request; a late released is ignored.
		s.Phase = Idle
		return done(s, effect(SendAbort), effect(PublishStatus))
	case eligible && !was && s.Phase == Idle:
		if !s.Enrolled {
			if nonce == "" {
				return done(s, effect(PublishStatus))
			}
			return acquire(s, nonce, Standalone)
		}
		// Back (resume, lid open): reconcile first; the laptop's stability interval starts now.
		s.Phase = Recovering
		return done(s, effect(Reconcile), schedule(StabilityTimer, LaptopStabilityMs))
	}
	return done(s, effect(PublishStatus))
}

func peerStatus(s State, d EventData) Transition {
	s.PeerReachable, s.PeerEligible = d.Reachable, d.Eligible
	s.PeerPinned = d.Pinned && s.Role == Laptop
	switch {
	case s.Phase == Active:
		// Both arms are ours: a peer claiming them is stale or split; reconcile, never record it.
		s.PeerActive = false
		if d.Active {
			return done(s, effect(Reconcile))
		}
	case s.Phase == Acquiring && d.Active && d.Reachable:
		// Another host holds the arms after all: back off; only its release may hand over.
		s.PeerActive = true
		return rollback(s)
	default:
		s.PeerActive = d.Active
		if s.Phase == Recovering && d.Reachable {
			s.Phase = Idle
		}
	}
	return done(s, effect(PublishStatus))
}

// preferLocal asks the peer to release. The laptop asks once it has been
// eligible for the stability interval and the phone is not pinned; the phone
// asks when it is pinned or the laptop is not eligible.
func preferLocal(s State, stableMs int64, nonce string) Transition {
	if s.Phase != Idle || !s.Enrolled || !s.Eligible || !s.PeerReachable || nonce == "" {
		return ignore(s)
	}
	if s.Role == Laptop && (s.PeerPinned || stableMs < LaptopStabilityMs) {
		return ignore(s)
	}
	if s.Role == Phone && !s.Pinned && s.PeerEligible {
		return ignore(s)
	}
	return request(s, nonce)
}

func request(s State, nonce string) Transition {
	s.Phase, s.Transfer = Requesting, nonce
	s.Generation++
	return done(s, effect(SendRequest))
}

func releaseRequest(s State, d EventData) Transition {
	refuse := d.Transfer == "" || d.Target == s.Role ||
		s.Role == Phone && s.Pinned && d.Reason != "manual" ||
		s.Role == Laptop && s.Eligible && !(d.Reason == "manual" && d.PeerPinned)
	if refuse {
		return done(s, effect(PublishStatus))
	}
	switch s.Phase {
	case Active, Acquiring:
		return yield(s, d.Transfer, Handover, true)
	case Idle:
		// Nothing native is held: confirm through the same release path, without timers.
		s.Phase, s.Transfer, s.ReleaseIntent = Yielding, d.Transfer, Handover
		s.Generation++
		return done(s, effect(SendRelease), effect(ReleaseBoth))
	}
	return done(s, effect(PublishStatus))
}

// yield releases the arms for transfer: the renderer goes first, then both
// links within the release deadline; released is sent only on release_complete.
// notify sends the release notification to the peer first.
func yield(s State, transfer string, intent ReleaseIntent, notify bool) Transition {
	s.Phase, s.Transfer, s.Permission, s.ReleaseIntent = Yielding, transfer, NoPermission, intent
	s.RendererReady = false
	s.Generation++
	fx := []Effect{effect(CancelTimers)}
	if notify {
		fx = append(fx, effect(SendRelease))
	}
	fx = append(fx, effect(ReleaseBoth), schedule(ReleaseTimer, ReleaseDeadlineMs))
	return done(s, fx...)
}

// rollback abandons this host's own acquisition (failure, partial arms, timeout, stop).
func rollback(s State) Transition {
	s.Phase, s.Permission, s.ReleaseIntent = Yielding, NoPermission, Rollback
	s.RendererReady = false
	s.Generation++
	return done(s, effect(CancelTimers), effect(ReleaseBoth), schedule(ReleaseTimer, ReleaseDeadlineMs))
}

func acquire(s State, transfer string, permission Permission) Transition {
	s.Phase, s.Transfer, s.Permission = Acquiring, transfer, permission
	s.Generation++
	fx := []Effect{}
	if permission == Released {
		fx = append(fx, effect(SendAcquiring))
	}
	fx = append(fx, effect(AcquireBoth), schedule(AcquireTimer, AcquireDeadlineMs))
	return done(s, fx...)
}

func releaseComplete(s State, d EventData) Transition {
	if (s.Phase != Yielding && s.Phase != Degraded) || !d.BothDown || !d.WorkersStopped || !d.ReconnectDisabled {
		return ignore(s)
	}
	intent := s.ReleaseIntent
	s.Arms, s.RendererReady, s.ReleaseIntent = Arms{}, false, NoRelease
	switch {
	case intent == Rollback && s.Eligible:
		s.Phase = Recovering
		return done(s, effect(SendAbort), schedule(RetryTimer, RetryInitialMs))
	case intent == Rollback:
		s.Phase = Idle
		return done(s, effect(SendAbort))
	}
	s.Phase = Idle
	if !s.Enrolled {
		return done(s, effect(PublishStatus))
	}
	return done(s, effect(SendReleased))
}

func armsChanged(s State, arms Arms) Transition {
	switch s.Phase {
	case Acquiring:
		first := !s.Arms.any()
		s.Arms = arms
		if arms.any() && !arms.both() {
			if first {
				return done(s, schedule(PartialArmTimer, PartialArmsMs))
			}
			return done(s, effect(PublishStatus))
		}
		return done(s, effect(PublishStatus))
	case Active:
		if arms.both() {
			return ignore(s)
		}
		// An arm dropped: not active until both are back and repainted, within the partial-arm grace.
		s.Arms, s.Phase, s.RendererReady = arms, Acquiring, false
		return done(s, effect(PublishStatus), schedule(PartialArmTimer, PartialArmsMs), schedule(AcquireTimer, AcquireDeadlineMs))
	}
	return ignore(s)
}

func timeout(s State, timer Timer, elapsed int64, nonce string) Transition {
	extra := int64(0)
	if s.Role == Phone {
		extra = PhoneProbeExtraMs
	}
	switch timer {
	case AcquireTimer:
		if s.Phase == Acquiring && elapsed >= AcquireDeadlineMs {
			return rollback(s)
		}
	case PartialArmTimer:
		if s.Phase == Acquiring && !s.Arms.both() && elapsed >= PartialArmsMs {
			return rollback(s)
		}
	case ReleaseTimer:
		if s.Phase == Yielding && elapsed >= ReleaseDeadlineMs {
			// Still no confirmation: force the links down, but never report released for it.
			s.Phase = Degraded
			return done(s, effect(ForceDisconnect), effect(PublishStatus))
		}
	case PeerSuspect:
		if s.PeerReachable {
			return done(s, effect(Reconcile))
		}
		if s.Phase == Active {
			return done(s, effect(PublishStatus)) // a healthy owner keeps the arms
		}
		if elapsed >= PeerSuspectMs+extra {
			return probe(s, nonce)
		}
	case RetryTimer:
		if s.PeerReachable && s.Phase == Recovering {
			return done(s, effect(Reconcile))
		}
		if elapsed >= RetryInitialMs+extra {
			return probe(s, nonce)
		}
	case ColdStartTimer:
		if s.PeerReachable && s.Phase == Recovering {
			return done(s, effect(Reconcile))
		}
		if !s.PeerActive && elapsed >= coldStartMs(s.Role) {
			return probe(s, nonce)
		}
	}
	return ignore(s)
}

// probe tries the arms when no peer can be asked. It cannot preempt: an arm
// held by another host stays unavailable, and the acquire deadline rolls back.
func probe(s State, nonce string) Transition {
	if s.Phase != Recovering || !s.Eligible || s.PeerReachable || nonce == "" {
		return ignore(s)
	}
	return acquire(s, nonce, Probe)
}

func pin(s State, enabled bool, nonce string) Transition {
	if s.Role != Phone {
		return ignore(s)
	}
	s.Pinned = enabled
	if enabled && s.Phase == Idle && s.Enrolled && s.Eligible && s.PeerReachable && nonce != "" {
		return request(s, nonce)
	}
	return done(s, effect(PublishStatus))
}

func coldStartMs(r Role) int64 {
	if r == Phone {
		return PhoneColdStartMs
	}
	return LaptopColdStartMs
}

func effect(kind EffectKind) Effect { return Effect{Kind: kind} }

func schedule(t Timer, ms int64) Effect { return Effect{Kind: Schedule, Timer: t, DelayMs: ms} }

func ignore(s State) Transition { return done(s, effect(Ignore)) }

// done stamps the effects with the new state's generation and transfer.
func done(s State, fx ...Effect) Transition {
	for i := range fx {
		fx[i].Generation, fx[i].Transfer = s.Generation, s.Transfer
	}
	return Transition{State: s, Effects: fx}
}
