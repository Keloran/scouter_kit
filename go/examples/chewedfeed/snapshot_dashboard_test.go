//go:build emulator

package main

import (
	"context"
	"testing"
	"time"

	"scouterkit"
)

func TestSnapshotDashboardApplication(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name, remoteProject, wantProject string
		override                         bool
	}{
		{"remote", "alpha", "alpha", false},
		{"local-flags", "alpha", "beta", true},
		{"missing-project", "missing", "", false},
		{"missing-page", "alpha", "alpha", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := sessionOptions()
			o.managed = true
			o.rotate = 12 * time.Second
			o.snapshots = newSnapshotBridge()
			o.snapshotLocal = snapshotOverrides{rotate: test.override, project: test.override}
			c := fakeClient(t, `[{"slug":"alpha","name":"Alpha"},{"slug":"beta","name":"Beta"}]`)
			if test.override {
				o.rotate = 0
				o.project = "beta"
				c.project = "beta"
			}
			host := &glassesHost{o: o}
			commands := make(chan hostCommand)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var dashErr error
			go func() { defer close(done); dashErr = dashboard(ctx, host, o, c, commands) }()
			native := dashboardNative{host, commands, done}
			t.Cleanup(func() {
				native.Abort()
				native.Release()
				cancel()
				<-done
				if dashErr != nil {
					t.Error(dashErr)
				}
			})
			await := func(ready func(uiSnapshot) bool) uiSnapshot {
				t.Helper()
				end := time.Now().Add(5 * time.Second)
				for time.Now().Before(end) {
					s, ok := o.snapshots.export()
					if ok && ready(s) {
						return s
					}
					time.Sleep(time.Millisecond)
				}
				s, _ := o.snapshots.export()
				t.Fatalf("snapshot not applied: %+v", s)
				return s
			}
			await(func(uiSnapshot) bool { return true })
			nav := snapshotNav()
			remote := exportSnapshot(&nav, test.remoteProject, 17*time.Second, 7)
			remote.View, remote.PageID, remote.RotationPaused = "stats", "system/cpu", false
			if test.name == "missing-page" {
				remote.PageID = "system/missing"
			}
			o.snapshots.requested("session-a", "t1")
			if !o.snapshots.receive("session-a", "t1", snapshotBody(t, remote)) {
				t.Fatal("staging failed")
			}
			o.snapshots.finish(true)
			// State transfer by itself never creates a native session.
			await(func(s uiSnapshot) bool {
				project := ""
				if s.Project != nil {
					project = *s.Project
				}
				return s.View == "stats" && project == test.wantProject
			})
			if host.Status().State != hostReleased {
				t.Fatal("snapshot acquired native handles")
			}
			acquireCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			arms, err := native.Acquire(acquireCtx)
			if err != nil {
				t.Fatalf("acquire after snapshot: %+v %v", arms, err)
			}
			// The controller polls Readiness while the first frame is in flight.
			for (!arms.Left || !arms.Right) && acquireCtx.Err() == nil {
				time.Sleep(time.Millisecond)
				arms = native.Readiness()
			}
			if !arms.Left || !arms.Right {
				t.Fatalf("not ready after snapshot: %+v", scouterkit.Left.State())
			}
			s := await(func(s uiSnapshot) bool { return s.PageID == "system/cpu" })
			wantRotation := int64(17000)
			if test.override {
				wantRotation = 0
			}
			if s.RotationMs != wantRotation || s.RotationPaused != test.override {
				t.Fatalf("rotation override lost: %+v", s)
			}
			if !scouterkit.Left.State().Connected || scouterkit.Left.State().ShownUpdate == 0 {
				t.Fatal("ready before rendering")
			}
			if err := native.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
