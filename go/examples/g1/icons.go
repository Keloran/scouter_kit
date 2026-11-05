package main

import (
	"embed"
	"path"
	"strings"
	"unicode"
)

//go:generate go run icons_gen.go

// The popups' app icons: Pebble's notification icons (Apache-2.0,
// ../../../third_party/pebble_icons), 50 x 50 at 4 bpp.
//
//go:embed icons/*.4bpp
var iconFiles embed.FS

const (
	iconSize    = 50
	iconStride  = iconSize / 2
	iconGap     = 12 // px from the icon to the text
	genericIcon = "Generic_notification"
)

// iconData is the bitmap of the icon (nil: no such icon).
func iconData(name string) []byte {
	b, err := iconFiles.ReadFile("icons/" + name + ".4bpp")
	if err != nil || len(b) != iconStride*iconSize {
		return nil
	}
	return b
}

// iconWords: the first icon one of whose words is a word of the
// notification's desktop entry, app name, icon name or category.
var iconWords = []struct{ icon, words string }{
	{"Telegram_app", "telegram telegramdesktop"},
	{"WhatsApp_notification", "whatsapp zapzap whatsie whatsdesk"},
	{"Signal", "signal"},
	{"Discord", "discord vesktop webcord armcord legcord"},
	{"Element", "element riot"},
	{"Slack", "slack"},
	{"Teams", "teams"},
	{"Zoom", "zoom"},
	{"Steam", "steam"},
	{"Viber", "viber"},
	{"WeChat", "wechat"},
	{"Facebook_messenger_notification", "messenger caprine"},
	{"Missed_call", "missed unanswered"},
	{"Incoming_call", "call calls phone dialer"},
	{"Calendar", "calendar korganizer kalendar merkuro appointment"},
	{"Generic_reminder", "reminder reminders todo tasks endeavour planify errands"},
	{"Generic_e-mail", "mail email gmail thunderbird betterbird evolution geary kmail mailspring claws sylpheed " +
		"mutt neomutt aerc trojita balsa"},
	{"Alarm_clock", "alarm alarms clock clocks kclock timer timers stopwatch"},
	{"Music_event", "music spotify rhythmbox lollypop elisa amarok strawberry audacious quodlibet clementine " +
		"cmus mpd ncmpcpp amberol tauon audio"},
	{"Generic_weather", "weather kweather meteo"},
	{"News_event", "news newsflash liferea akregator feeds rss quiterss"},
	{"Generic_SMS", "im chat messages message sms pidgin hexchat polari fractal mattermost zulip kdeconnect " +
		"chatty dino gajim irc matrix xmpp konversation weechat quassel"},
	{"Settings", "update updates updater software discover packagekit pamac settings"},
	{"Generic_warning", "warning error critical caution battery power fail failed"},
	{"Generic_confirmation", "complete completed done finished success ok"},
}

// words splits names such as "org.telegram.desktop", "Telegram Desktop" or
// "battery-caution-symbolic" into lower-case words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// iconFor picks the icon of a notification: by its words, else the warning
// for a critical one, else the generic one.
func iconFor(n *note) string {
	icon := n.Icon
	if strings.Contains(icon, "/") {
		icon = strings.TrimSuffix(path.Base(icon), path.Ext(icon))
	}
	seen := map[string]bool{}
	for _, s := range []string{n.Desktop, n.App, icon, n.Category} {
		for _, w := range words(s) {
			seen[w] = true
		}
	}
	for _, g := range iconWords {
		for _, w := range strings.Fields(g.words) {
			if seen[w] {
				return g.icon
			}
		}
	}
	if n.Urgency >= 2 {
		return "Generic_warning"
	}
	return genericIcon
}
