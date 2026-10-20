package main

import (
	"strconv"
	"time"
)

// clientStartFacts is a redacted, typed snapshot of a started child, taken from
// the process handle that exec.Cmd retains. It carries numbers, a closed enum
// and explicit known/unknown marks only: never argv, environment, credentials,
// account/provider text or a raw inner error.
//
// Four defects of the 185 draft are fixed here:
//  1. exit code 259 (STILL_ACTIVE) is no longer used as the liveness test; the
//     handle is asked via WaitForSingleObject first, so a real exit 259 is Exited.
//  2. birth keeps full FILETIME (100ns ticks since 1601) precision, not only a
//     second-resolution text form.
//  3. HasExitCode distinguishes "no proof" from "exit 0": an unknown exit code is
//     never rendered as exit_code=0.
//  4. the handle probe result is named separately from the controller inspect, so
//     a successful handle read is never presented as the controller inspect
//     having succeeded.
type clientStartFacts struct {
	PID             int
	Probed          bool
	Exited          bool
	HasExitCode     bool
	ExitCode        int
	StillRunning    bool
	HasCreated      bool
	Created         time.Time
	CreatedFiletime uint64
	Inspect         string
	BirthInspect    string
}

func (f clientStartFacts) stateWord() string {
	switch {
	case !f.Probed:
		return "unknown"
	case f.Exited:
		return "exited"
	case f.StillRunning:
		return "still_running"
	default:
		return "unknown"
	}
}

func (f clientStartFacts) inspectWord() string {
	if f.Inspect == "" {
		return "not_probed"
	}
	return f.Inspect
}

// birthWord reports the birth probe separately from the liveness probe, so a
// successful liveness read never implies a known birth.
func (f clientStartFacts) birthWord() string {
	if !f.HasCreated {
		if f.BirthInspect == "" {
			return "unavailable"
		}
		return f.BirthInspect
	}
	return "ok"
}

// CreatedUTC converts the stored FILETIME to wall clock without losing precision.
func (f clientStartFacts) CreatedUTC() time.Time {
	if f.HasCreated {
		return f.Created
	}
	return time.Time{}
}

// Describe renders the facts with explicit unknown tokens. It never prints a
// fabricated zero: an exit code is only printed when HasExitCode is set.
func (f clientStartFacts) Describe() string {
	out := "state=" + f.stateWord()
	if f.HasExitCode {
		out += " exit_code=" + strconv.Itoa(f.ExitCode)
	} else {
		out += " exit_code=unknown"
	}
	if f.HasCreated {
		out += " created_utc=" + f.CreatedText()
		out += " created_filetime=" + strconv.FormatUint(f.CreatedFiletime, 10)
	} else {
		out += " created_utc=unknown created_filetime=unknown"
	}
	return out + " liveness_inspect=" + f.inspectWord() + " birth_inspect=" + f.birthWord()
}

// CreatedText renders the birth with full FILETIME precision.
func (f clientStartFacts) CreatedText() string {
	if !f.HasCreated {
		return "unknown"
	}
	return f.Created.UTC().Format(time.RFC3339Nano)
}
