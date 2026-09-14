// Package caldav implements a CalDAV backend over Proton Calendar.
//
// Proton stores events as iCalendar split across SharedEvents, CalendarEvents,
// AttendeesEvents and PersonalEvents, each with signed and encrypted sections
// under a per-calendar key. Reading means reassembling those parts into one
// VEVENT; writing means splitting a VEVENT back across them correctly.
package caldav
