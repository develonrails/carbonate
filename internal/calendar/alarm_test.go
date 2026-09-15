package calendar

import (
	"strings"
	"testing"
)

const withAlarm = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//carbonate//test//EN
BEGIN:VEVENT
UID:alarm@carbonate.local
DTSTAMP:20260915T080000Z
DTSTART:20260930T090000Z
SUMMARY:Standup
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
DESCRIPTION:Reminder
END:VALARM
END:VEVENT
END:VCALENDAR
`

// A reminder used to be accepted and thrown away without a word, because
// VALARM is a child component and the split works on properties.
func TestAlarmsAreCollected(t *testing.T) {
	got := alarmsOf(parse(t, withAlarm))

	if len(got) != 1 {
		t.Fatalf("collected %d alarms, want 1", len(got))
	}

	if got[0].Trigger != "-PT15M" {
		t.Errorf("trigger = %q, want %q", got[0].Trigger, "-PT15M")
	}

	if got[0].Type != notifyDevice {
		t.Errorf("type = %d, want a device notification", got[0].Type)
	}
}

func TestEmailAlarmKeepsItsKind(t *testing.T) {
	ics := strings.Replace(withAlarm, "ACTION:DISPLAY", "ACTION:EMAIL", 1)

	got := alarmsOf(parse(t, ics))

	if len(got) != 1 || got[0].Type != notifyEmail {
		t.Errorf("alarms = %+v, want one email notification", got)
	}
}

// Proton knows only two kinds. An AUDIO alarm becomes a device notification,
// since a reminder that appears is closer to the request than none at all.
func TestUnknownActionBecomesADeviceAlarm(t *testing.T) {
	ics := strings.Replace(withAlarm, "ACTION:DISPLAY", "ACTION:AUDIO", 1)

	got := alarmsOf(parse(t, ics))

	if len(got) != 1 || got[0].Type != notifyDevice {
		t.Errorf("alarms = %+v, want one device notification", got)
	}
}

// An alarm without a trigger would never fire; sending it would promise a
// reminder that cannot happen.
func TestAlarmWithoutTriggerIsDropped(t *testing.T) {
	ics := strings.Replace(withAlarm, "TRIGGER:-PT15M\n", "", 1)

	if got := alarmsOf(parse(t, ics)); len(got) != 0 {
		t.Errorf("alarms = %+v, want none", got)
	}
}

func TestEventWithoutAlarmsCollectsNone(t *testing.T) {
	got := alarmsOf(parse(t, fullEvent))

	if len(got) != 0 {
		t.Errorf("alarms = %+v, want none", got)
	}

	// Not nil: the field is always sent, so that removing the last alarm on
	// an update clears it instead of leaving the old one in place.
	if got == nil {
		t.Error("alarms is nil, which would be omitted from the request")
	}
}

func TestAlarmsRenderBackAsValarm(t *testing.T) {
	lines := alarmLines([]notification{{Type: notifyDevice, Trigger: "-PT15M"}}, "Standup")

	joined := strings.Join(lines, "\r\n")

	for _, want := range []string{"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT15M", "DESCRIPTION:Standup", "END:VALARM"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rendered alarm is missing %q:\n%s", want, joined)
		}
	}
}

// RFC 5545 requires a DISPLAY alarm to carry a description, and an event with
// no summary still needs one.
func TestAlarmAlwaysHasADescription(t *testing.T) {
	joined := strings.Join(alarmLines([]notification{{Trigger: "-PT5M"}}, ""), "\r\n")

	if !strings.Contains(joined, "DESCRIPTION:") {
		t.Errorf("alarm has no description:\n%s", joined)
	}

	if strings.Contains(joined, "DESCRIPTION:\r\n") || strings.HasSuffix(joined, "DESCRIPTION:") {
		t.Errorf("alarm description is empty:\n%s", joined)
	}
}

// The whole point: what goes in comes back out.
func TestAlarmRoundTrip(t *testing.T) {
	event := parse(t, withAlarm)

	e := Event{Alarms: alarmsOf(event)}
	e.Properties = properties(pick(event, sharedEncrypted))

	ics := e.ICS()

	if !strings.Contains(ics, "BEGIN:VALARM") {
		t.Fatalf("the reminder did not survive:\n%s", ics)
	}

	if !strings.Contains(ics, "TRIGGER:-PT15M") {
		t.Errorf("the trigger did not survive:\n%s", ics)
	}

	// The alarm must sit inside the event, not beside it.
	if strings.Index(ics, "BEGIN:VALARM") > strings.Index(ics, "END:VEVENT") {
		t.Errorf("the alarm is outside the event:\n%s", ics)
	}
}
