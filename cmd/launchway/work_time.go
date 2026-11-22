package main

import (
	"fmt"
	"os"
	"strconv"
)

// launchway work time ...: timers, entries, totals and the CSV export.

func (cl *client) workTime(args []string) error {
	name, rest := args[0], args[1:]
	switch name {
	case "show":
		return cl.workDo("time show", rest, timeShow)
	case "start":
		return cl.workDo("time start", rest, timeStart)
	case "stop":
		return cl.workDo("time stop", rest, timeStop)
	case "running":
		return cl.workDo("time running", rest, timeRunning)
	case "log":
		return cl.workDo("time log", rest, timeLog)
	case "edit":
		return cl.workDo("time edit", rest, timeEdit)
	case "delete":
		return cl.workDo("time delete", rest, timeDelete)
	case "report":
		return cl.workDo("time report", rest, timeReport)
	case "export":
		return cl.workTimeExport(rest)
	}
	return usageErr("unknown command work time %s\n\n%s", name, workUsage)
}

func hm(minutes string) string {
	n, _ := strconv.Atoi(minutes)
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

func timeShow(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work time show REF")
		}
		return &workReq{method: "GET", path: apiItem(pos[0]) + "/time", show: func(out any) {
			rows := [][]string{}
			for _, e := range wlist(out, "entries") {
				rows = append(rows, []string{wstr(e, "id"), wstr(e, "day"), wstr(e, "who"), hm(wstr(e, "minutes")), wstr(e, "kind"), wstr(e, "note")})
			}
			w.cl.wtable("ID\tDAY\tWHO\tTIME\tKIND\tNOTE", rows)
			fmt.Fprintf(w.cl.out, "\nLogged %s", hm(wstr(out, "total_minutes")))
			if wstr(out, "remaining_hours") != "" {
				fmt.Fprintf(w.cl.out, "; estimate %s h, remaining %s h", wstr(out, "estimate"), wstr(out, "remaining_hours"))
			} else if n := wstr(out, "note"); n != "" {
				fmt.Fprintf(w.cl.out, " (%s)", n)
			}
			fmt.Fprintln(w.cl.out)
			for _, r := range wlist(out, "running") {
				fmt.Fprintf(w.cl.out, "%s has a timer running since %s\n", wstr(r, "who"), wstr(r, "started_at"))
			}
		}}, nil
	}
}

func timeStart(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work time start REF")
		}
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/timer", show: func(out any) {
			fmt.Fprintf(w.cl.out, "Timer started on %s.\n", wstr(out, "item"))
		}}, nil
	}
}

func timeStop(w *wcmd) func([]string) (*workReq, error) {
	note := w.fs.String("note", "", "what was done")
	return func([]string) (*workReq, error) {
		b := map[string]any{}
		setIf(b, "note", *note)
		return &workReq{method: "POST", path: "/api/v1/work/timer/stop", body: b, show: func(out any) {
			capped := ""
			if wstr(out, "capped") == "true" {
				capped = " (the timer ran for more than a day: counted as a day; correct it with work time edit)"
			}
			fmt.Fprintf(w.cl.out, "Logged %s on %s%s.\n", hm(wstr(out, "minutes")), wstr(out, "item"), capped)
		}}, nil
	}
}

func timeRunning(w *wcmd) func([]string) (*workReq, error) {
	return func([]string) (*workReq, error) {
		return &workReq{method: "GET", path: "/api/v1/work/timer", show: func(out any) {
			t := wget(out, "timer")
			if t == nil {
				fmt.Fprintln(w.cl.out, "No timer running.")
				return
			}
			fmt.Fprintf(w.cl.out, "Timing %s since %s (%s s).\n", wstr(t, "item"), wstr(t, "started_at"), wstr(t, "seconds"))
		}}, nil
	}
}

func timeLog(w *wcmd) func([]string) (*workReq, error) {
	day := w.fs.String("day", "", "the day, 2026-12-31 (default today)")
	note := w.fs.String("note", "", "what was done")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 2 {
			return nil, usageErr("launchway work time log REF MINUTES [--day D] [--note T]")
		}
		n, err := strconv.Atoi(pos[1])
		if err != nil {
			return nil, usageErr("MINUTES is a whole number of minutes")
		}
		b := map[string]any{"minutes": n}
		setIf(b, "day", *day)
		setIf(b, "note", *note)
		return &workReq{method: "POST", path: apiItem(pos[0]) + "/time", body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Logged %s on %s (entry %s).\n", hm(wstr(out, "minutes")), wstr(out, "item"), wstr(out, "id"))
		}}, nil
	}
}

func timeEdit(w *wcmd) func([]string) (*workReq, error) {
	minutes := w.fs.Int("minutes", 0, "the new minutes")
	day := w.fs.String("day", "", "the new day")
	note := w.fs.String("note", "", "the new note")
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work time edit ENTRY_ID [--minutes N] [--day D] [--note T]")
		}
		b := map[string]any{}
		if *minutes != 0 {
			b["minutes"] = *minutes
		}
		setIf(b, "day", *day)
		if w.given()["note"] {
			b["note"] = *note
		}
		return &workReq{method: "PATCH", path: "/api/v1/work/time/" + urlPath(pos[0]), body: b, show: func(out any) {
			fmt.Fprintf(w.cl.out, "Entry %s is now %s on %s.\n", wstr(out, "id"), hm(wstr(out, "minutes")), wstr(out, "day"))
		}}, nil
	}
}

func timeDelete(w *wcmd) func([]string) (*workReq, error) {
	return func(pos []string) (*workReq, error) {
		if len(pos) != 1 {
			return nil, usageErr("launchway work time delete ENTRY_ID")
		}
		return &workReq{method: "DELETE", path: "/api/v1/work/time/" + urlPath(pos[0]), show: func(out any) {
			fmt.Fprintf(w.cl.out, "Deleted %s.\n", wstr(out, "deleted"))
		}}, nil
	}
}

func timeQueryPath(w *wcmd, base string, person, from, to *string) string {
	return base + q("scope", w.scopeValue(), "person", *person, "from", *from, "to", *to)
}

func timeReport(w *wcmd) func([]string) (*workReq, error) {
	w.useScope()
	person := w.fs.String("person", "", "one person, or me")
	from := w.fs.String("from", "", "first day, 2026-12-31")
	to := w.fs.String("to", "", "last day")
	return func([]string) (*workReq, error) {
		return &workReq{method: "GET", path: timeQueryPath(w, "/api/v1/work/time", person, from, to), show: func(out any) {
			rows := [][]string{}
			for _, p := range wlist(out, "people") {
				rows = append(rows, []string{wstr(p, "who"), hm(wstr(p, "minutes")), strconv.Itoa(len(wlist(p, "items"))) + " items"})
			}
			w.cl.wtable("PERSON\tTIME\tON", rows)
			fmt.Fprintf(w.cl.out, "\nTotal %s.\n", hm(wstr(out, "total_minutes")))
			if wstr(out, "truncated") == "true" {
				fmt.Fprintln(w.cl.out, "More entries than a report holds: narrow the days.")
			}
		}}, nil
	}
}

func (cl *client) workTimeExport(args []string) error {
	return cl.workDo("time export", args, func(w *wcmd) func([]string) (*workReq, error) {
		w.useScope()
		person := w.fs.String("person", "", "one person, or me")
		from := w.fs.String("from", "", "first day")
		to := w.fs.String("to", "", "last day")
		out := w.fs.String("out", "", "write it to this file (default: standard output)")
		return func([]string) (*workReq, error) {
			data, err := w.cl.raw(timeQueryPath(w, "/api/v1/work/time/export", person, from, to))
			if err != nil {
				return nil, err
			}
			if *out == "" {
				_, err = w.cl.out.Write(data)
				return &workReq{}, err
			}
			return &workReq{}, os.WriteFile(*out, data, 0o600)
		}
	})
}
