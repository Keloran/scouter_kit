package main

import (
	"context"
	"time"
)

type apiSelection struct {
	project    string
	generation uint64
}
type apiUpdate struct {
	selection apiSelection
	data      snapshot
}

// The worker owns its client copy and previous rows. A project change never
// mutates a client being fetched by another goroutine, or reuses its cache.
func watchDashboardAPI(ctx context.Context, c *client, refresh time.Duration, selection apiSelection,
	requests <-chan apiSelection, updates chan<- apiUpdate) {
	var previous snapshot
	for {
	drain:
		for {
			select {
			case selection = <-requests:
				previous = nil
			default:
				break drain
			}
		}
		local := *c
		local.project = selection.project
		previous = local.fetch(ctx, previous)
		select {
		case updates <- apiUpdate{selection, previous}:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(refresh)
		select {
		case <-timer.C:
		case selection = <-requests:
			timer.Stop()
			previous = nil
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func selectDashboardProject(requests chan apiSelection, selection apiSelection) {
	select {
	case <-requests:
	default:
	}
	requests <- selection // only the serialized UI writes; capacity one
}

func missingSnapshotProject(data snapshot) bool {
	for _, r := range data {
		if r.key == "projects" {
			return r.err == "project slug not found"
		}
	}
	return false
}
