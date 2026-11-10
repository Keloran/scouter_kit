package main

const (
	viewSites = iota
	viewAI
	viewNotifications
	viewSystem
)

type viewPages struct {
	name  string
	pages []page
	index int
}

type navigation struct {
	views          []viewPages
	selected       int
	paused, hidden bool
	inCase         bool
	level          bool // head between the show angles: dashboard off, popups still shown
}

func newNavigation(sites, ai, system []page, paused bool) navigation {
	return navigation{views: []viewPages{{name: "Sites", pages: sites}, {name: "AI", pages: ai},
		{name: "Notifications", pages: emptyNotificationHistory()},
		{name: "Stats", pages: system}}, paused: paused}
}

func (n *navigation) current() *viewPages { return &n.views[n.selected] }

func (n *navigation) visible() bool { return n.popupVisible() && !n.level }

func (n *navigation) popupVisible() bool { return !n.hidden && !n.inCase }

func (n *navigation) replace(view int, pages []page) {
	if len(pages) == 0 {
		return
	}
	v := &n.views[view]
	id := v.pages[v.index].id
	v.pages, v.index = pages, preservePage(pages, id)
}

func (n *navigation) move(step int) {
	v := n.current()
	v.index = (v.index + step + len(v.pages)) % len(v.pages)
}

// The firmware reports classified gestures: a double tap has no preceding single tap.
func (n *navigation) touch(gesture uint32, left bool) bool {
	if n.inCase {
		return false
	}
	switch gesture {
	case 1:
		step := 1
		if left {
			step = -1
		}
		n.move(step)
	case 2:
		step := 1
		if left {
			step = -1
		}
		n.selected = (n.selected + step + len(n.views)) % len(n.views)
	case 3:
		if left {
			n.hidden = !n.hidden
		} else {
			n.paused = !n.paused
		}
	default:
		return false
	}
	return true
}
