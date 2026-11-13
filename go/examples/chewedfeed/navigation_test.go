package main

import "testing"

func TestTouchNavigationRemembersPagesAcrossScreens(t *testing.T) {
	n := newNavigation([]page{{id: "a"}, {id: "b"}, {id: "c"}}, []page{{id: "codex"}, {id: "claude"}}, []page{{id: "cpu"}, {id: "memory"}}, false)
	n.touch(1, false)
	if n.current().pages[n.current().index].id != "b" {
		t.Fatal("single tap did not move to the next site")
	}
	n.touch(2, false)
	if n.selected != 1 || n.current().index != 0 || n.paused {
		t.Fatal("double tap should switch screens without moving its page or pausing")
	}
	n.touch(1, false)
	n.touch(2, true)
	if n.current().pages[n.current().index].id != "b" {
		t.Fatal("switching back lost the site's reading position")
	}
	n.touch(3, false)
	n.touch(2, false)
	if n.current().pages[n.current().index].id != "claude" || !n.paused {
		t.Fatal("AI reading position or triple-tap pause was lost")
	}
	n.touch(1, true)
	if n.current().pages[n.current().index].id != "codex" {
		t.Fatal("left tap did not move back a page")
	}
	for _, left := range []bool{false, true} {
		if n.touch(4, left) || n.touch(5, left) {
			t.Fatal("long press and release should be ignored on either arm")
		}
	}
}

func TestScreenRefreshPreservesSelectionAndPageIdentity(t *testing.T) {
	n := newNavigation([]page{{id: "a"}, {id: "b"}}, []page{{id: "codex"}, {id: "claude"}}, []page{{id: "cpu"}}, false)
	n.touch(1, false)
	n.touch(2, false)
	n.touch(1, false)
	n.replace(0, []page{{id: "b"}, {id: "a"}})
	n.replace(1, []page{{id: "claude"}, {id: "codex"}})
	if n.selected != 1 || n.current().pages[n.current().index].id != "claude" || n.views[0].index != 0 {
		t.Fatal("background refresh changed the selected screen or reading position")
	}
	n.move(1)
	if n.selected != 1 || n.current().pages[n.current().index].id != "codex" {
		t.Fatal("automatic rotation should advance only the selected screen")
	}
	n.replace(1, nil)
	if len(n.current().pages) != 2 {
		t.Fatal("empty update removed the active screen")
	}
}

func TestScreenCycleBothDirectionsAndDisplayToggle(t *testing.T) {
	n := newNavigation([]page{{id: "site"}}, []page{{id: "ai"}}, []page{{id: "cpu"}, {id: "memory"}}, false)
	for _, want := range []int{viewAI, viewNotifications, viewSystem, viewSites} {
		n.touch(2, false)
		if n.selected != want {
			t.Fatalf("right screen cycle: got %d, want %d", n.selected, want)
		}
	}
	for _, want := range []int{viewSystem, viewNotifications, viewAI, viewSites} {
		n.touch(2, true)
		if n.selected != want {
			t.Fatalf("left screen cycle: got %d, want %d", n.selected, want)
		}
	}
	n.touch(2, true)
	n.touch(1, false)
	n.touch(2, false)
	n.touch(2, true)
	if n.current().pages[n.current().index].id != "memory" {
		t.Fatal("screen cycle lost reading position")
	}
	if n.touch(3, true) || n.paused {
		t.Fatal("left triple tap should do nothing")
	}
	n.touch(3, false)
	if !n.visible() || !n.paused {
		t.Fatal("right triple tap should pause without changing visibility")
	}
	n.replace(viewSystem, []page{{id: "memory"}, {id: "cpu"}})
	if n.selected != viewSystem || n.current().pages[n.current().index].id != "memory" {
		t.Fatal("system refresh lost selection or page identity")
	}
}
