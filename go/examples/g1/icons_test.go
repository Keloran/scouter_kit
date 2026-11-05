package main

import "testing"

func TestIconFor(t *testing.T) {
	for _, c := range []struct {
		n    note
		want string
	}{
		{note{App: "Telegram Desktop", Desktop: "org.telegram.desktop"}, "Telegram_app"},
		{note{App: "Thunderbird", Desktop: "org.mozilla.Thunderbird"}, "Generic_e-mail"},
		{note{App: "Mail", Desktop: "io.elementary.mail"}, "Generic_e-mail"},
		{note{App: "Element", Desktop: "io.element.Element"}, "Element"},
		{note{App: "Signal", Desktop: "signal-desktop"}, "Signal"},
		{note{App: "Evolution Reminders", Desktop: "org.gnome.Evolution-alarm-notify"}, "Generic_reminder"},
		{note{App: "Calls", Desktop: "org.gnome.Calls", Category: "call.unanswered"}, "Missed_call"},
		{note{App: "kdeconnect", Category: "im.received"}, "Generic_SMS"},
		{note{App: "notify-send", Icon: "dialog-warning"}, "Generic_warning"},
		{note{Icon: "/usr/share/icons/hicolor/48x48/apps/spotify-client.png"}, "Music_event"},
		{note{App: "g1 notify", Icon: "done"}, "Generic_confirmation"},
		{note{App: "g1 check"}, genericIcon},
		{note{App: "make", Urgency: 2}, "Generic_warning"},
	} {
		if got := iconFor(&c.n); got != c.want {
			t.Errorf("%+v: %s, want %s", c.n, got, c.want)
		}
	}
}

// Every icon the table names is there, 50 x 50 at 4 bpp.
func TestIconFiles(t *testing.T) {
	names := []string{genericIcon}
	for _, g := range iconWords {
		names = append(names, g.icon)
	}
	for _, n := range names {
		if iconData(n) == nil {
			t.Errorf("no icon %s", n)
		}
	}
	files, err := iconFiles.ReadDir("icons")
	if err != nil || len(files) < len(iconWords) {
		t.Fatalf("%d icon files, %v", len(files), err)
	}
}
