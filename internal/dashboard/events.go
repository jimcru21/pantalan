package dashboard

import (
	"net/http"
	"strings"

	"github.com/pantalan/pantalan/internal/nodeinfo"
)

// Events is Docker's recent container events; nodeinfo.EventStream
// provides it.
type Events interface {
	Recent() nodeinfo.EventLog
}

// eventsTab is the dot of the "events" block.
type eventsTab struct {
	Nav tabNav
	// StreamErr is why the event stream is down, while it is; the ring
	// still shows.
	StreamErr string
	Rows      []eventRow
}

type eventRow struct {
	Time      string // UTC, 15:04:05
	Container string
	Action    string // "health_status", without its status
	Detail    string // exit code, signal or health status
	Image     string
	// GapBefore puts the "stream reconnected" marker under the row: the
	// events before it may be missing.
	GapBefore bool
}

func (d *Dashboard) eventsData(*http.Request) (any, error) {
	tab := eventsTab{Nav: nav("events")}
	if d.deps.Events == nil {
		return tab, nil
	}
	log := d.deps.Events.Recent()
	if log.Err != nil {
		tab.StreamErr = log.Err.Error()
	}
	for _, ev := range log.Events {
		attrs := ev.Actor.Attributes
		row := eventRow{
			Time:      ev.Time().UTC().Format("15:04:05"),
			Container: attrs["name"],
			Action:    ev.Action,
			Image:     attrs["image"],
			GapBefore: ev.GapBefore,
		}
		if row.Container == "" {
			row.Container = ev.Actor.ID
		}
		if action, status, ok := strings.Cut(ev.Action, ": "); ok {
			row.Action, row.Detail = action, status
		}
		switch row.Action {
		case "die":
			if code, ok := attrs["exitCode"]; ok {
				row.Detail = "exit code " + code
			}
		case "kill":
			if sig, ok := attrs["signal"]; ok {
				row.Detail = "signal " + sig
			}
		}
		tab.Rows = append(tab.Rows, row)
	}
	return tab, nil
}
