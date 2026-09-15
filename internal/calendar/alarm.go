package calendar

import (
	"strings"

	"github.com/emersion/go-ical"
)

// Proton does not keep reminders in an iCalendar part. They travel as a
// separate Notifications field on the event, beside the encrypted cards, so
// the split that handles properties never sees a VALARM.
//
// Type is Proton's notification kind; Trigger is the iCalendar TRIGGER value
// verbatim, such as "-PT15M".
type notification struct {
	Type    int    `json:"Type"`
	Trigger string `json:"Trigger"`
}

// Proton's notification kinds.
const (
	notifyEmail  = 0
	notifyDevice = 1
)

// alarmsOf turns the VALARM children of an event into Proton notifications.
//
// An alarm with no trigger is meaningless and is dropped rather than sent as
// a reminder that would never fire.
func alarmsOf(event *ical.Event) []notification {
	out := []notification{}

	for _, child := range event.Children {
		if !strings.EqualFold(child.Name, ical.CompAlarm) {
			continue
		}

		trigger := child.Props.Get("TRIGGER")
		if trigger == nil || strings.TrimSpace(trigger.Value) == "" {
			continue
		}

		out = append(out, notification{
			Type:    notificationType(child.Props.Get("ACTION")),
			Trigger: strings.TrimSpace(trigger.Value),
		})
	}

	return out
}

// notificationType maps an iCalendar alarm action onto Proton's kinds.
//
// Proton knows only "email me" and "tell the device", so AUDIO and anything
// unrecognised become a device notification: a reminder that shows up is
// closer to what was asked for than none at all.
func notificationType(action *ical.Prop) int {
	if action != nil && strings.EqualFold(strings.TrimSpace(action.Value), "EMAIL") {
		return notifyEmail
	}

	return notifyDevice
}

// alarmLines renders Proton's notifications back as VALARM components.
//
// summary becomes the alarm's description, which RFC 5545 requires for a
// DISPLAY alarm and which is what a client shows when it fires.
func alarmLines(notifications []notification, summary string) []string {
	if summary == "" {
		summary = "Reminder"
	}

	var out []string

	for _, n := range notifications {
		if strings.TrimSpace(n.Trigger) == "" {
			continue
		}

		action := "DISPLAY"
		if n.Type == notifyEmail {
			action = "EMAIL"
		}

		out = append(out,
			"BEGIN:VALARM",
			"ACTION:"+action,
			"TRIGGER:"+n.Trigger,
			"DESCRIPTION:"+summary,
			"END:VALARM",
		)
	}

	return out
}
