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
	e.Properties = properties(pick(event, sharedEncrypted, false))

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

// An alarm set for a fixed moment has to become an offset before it is sent.
//
// Proton's Trigger field holds a duration and nothing else, so an absolute
// trigger stored verbatim is a reminder its own clients cannot read.
func TestAbsoluteTriggerBecomesRelative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		want    string
	}{
		{"half an hour before", "20260930T083000Z", "-PT30M"},
		{"a day and a bit before", "20260929T070000Z", "-P1DT2H"},
		{"after the event starts", "20260930T091500Z", "PT15M"},
		{"exactly at the start", "20260930T090000Z", "-PT0S"},
		{"a whole week before", "20260923T090000Z", "-P7D"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ics := strings.Replace(withAlarm, "TRIGGER:-PT15M", "TRIGGER;VALUE=DATE-TIME:"+tc.trigger, 1)

			got := alarmsOf(parse(t, ics))

			if len(got) != 1 {
				t.Fatalf("collected %d alarms, want 1", len(got))
			}

			if got[0].Trigger != tc.want {
				t.Errorf("trigger = %q, want %q", got[0].Trigger, tc.want)
			}
		})
	}
}

// A relative trigger is Proton's own format and must go through untouched.
func TestRelativeTriggerIsLeftAlone(t *testing.T) {
	for _, trigger := range []string{"-PT15M", "PT1H", "-P1DT2H30M"} {
		ics := strings.Replace(withAlarm, "TRIGGER:-PT15M", "TRIGGER:"+trigger, 1)

		got := alarmsOf(parse(t, ics))

		if len(got) != 1 || got[0].Trigger != trigger {
			t.Errorf("trigger %q came back as %+v", trigger, got)
		}
	}
}

// An alarm we cannot place in time is dropped rather than guessed at. A
// reminder at the wrong moment is worse than one the user can see is gone.
func TestUnplaceableAbsoluteTriggerIsDropped(t *testing.T) {
	ics := strings.Replace(withAlarm, "TRIGGER:-PT15M", "TRIGGER;VALUE=DATE-TIME:not-a-time", 1)

	if got := alarmsOf(parse(t, ics)); len(got) != 0 {
		t.Errorf("alarms = %+v, want none", got)
	}
}
