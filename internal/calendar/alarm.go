package calendar

import (
	"strconv"
	"strings"
	"time"

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

		prop := child.Props.Get("TRIGGER")
		if prop == nil || strings.TrimSpace(prop.Value) == "" {
			continue
		}

		trigger := strings.TrimSpace(prop.Value)

		if !isDuration(trigger) {
			relative, ok := relativeTrigger(prop, event)
			if !ok {
				continue
			}

			trigger = relative
		}

		out = append(out, notification{
			Type:    notificationType(child.Props.Get("ACTION")),
			Trigger: trigger,
		})
	}

	return out
}

// isDuration reports whether a TRIGGER value is an offset rather than a
// moment in time. RFC 5545 allows either; Proton stores only the first.
func isDuration(trigger string) bool {
	return strings.HasPrefix(trigger, "P") ||
		strings.HasPrefix(trigger, "-P") ||
		strings.HasPrefix(trigger, "+P")
}

// relativeTrigger converts an alarm set for a fixed moment into the offset
// from the event's start that Proton stores.
//
// Proton has nowhere to keep an absolute trigger: the Trigger field is a
// duration, and sending a date-time would store a reminder its own clients
// cannot read. Its web app converts the same way before writing
// (absoluteToRelativeTrigger), to the same resolution of whole minutes.
//
// An alarm whose moment, or whose event's start, cannot be read is dropped.
// A reminder at the wrong time is worse than one the user can see is
// missing, and a start in an unknown time zone cannot be subtracted from
// anything honestly.
func relativeTrigger(trigger *ical.Prop, event *ical.Event) (string, bool) {
	when, ok := absoluteTime(trigger)
	if !ok {
		return "", false
	}

	start := event.Props.Get("DTSTART")
	if start == nil {
		return "", false
	}

	from, ok := absoluteTime(start)
	if !ok {
		return "", false
	}

	return durationString(int(from.Sub(when).Minutes())), true
}

// absoluteTime reads a property as a moment in time, resolving TZID where
// there is one and treating a floating value as UTC.
func absoluteTime(prop *ical.Prop) (time.Time, bool) {
	when, err := prop.DateTime(time.UTC)
	if err != nil {
		return time.Time{}, false
	}

	return when, true
}

// durationString renders minutes before an event as an iCalendar duration.
//
// Positive minutes mean the alarm comes before the start, which is a
// negative duration — the sign convention Proton uses as well.
//
// Whole weeks are rendered as days rather than with a week component.
// Proton's own conversion emits weeks and days together, which RFC 5545
// does not allow in one duration; the offset is identical either way, and
// the days-only form is the one every client can read.
func durationString(minutes int) string {
	sign := ""
	if minutes >= 0 {
		sign = "-"
	}

	if minutes < 0 {
		minutes = -minutes
	}

	days, hours, mins := minutes/(24*60), minutes%(24*60)/60, minutes%60

	if days == 0 && hours == 0 && mins == 0 {
		return sign + "PT0S"
	}

	out := sign + "P"

	if days > 0 {
		out += strconv.Itoa(days) + "D"
	}

	if hours > 0 || mins > 0 {
		out += "T"

		if hours > 0 {
			out += strconv.Itoa(hours) + "H"
		}

		if mins > 0 {
			out += strconv.Itoa(mins) + "M"
		}
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
