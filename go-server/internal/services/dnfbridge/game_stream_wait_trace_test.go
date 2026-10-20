package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"longheng.io/server/internal/platform/config"
)

func TestGameStreamWaitTraceReportsStalledPublicHeaderOnce(t *testing.T) {
	// A complete op4 can be stuck behind an earlier partial frame. Inspect the
	// earlier public header without exposing either frame's body or modifying it.
	remaining := bytes.Repeat([]byte{0x5a}, 40)
	remaining[0] = 1
	binary.LittleEndian.PutUint16(remaining[1:3], 1518)
	binary.LittleEndian.PutUint32(remaining[3:7], 1024)
	before := bytes.Clone(remaining)
	start := time.Unix(100, 0)
	var trace gameStreamWaitTrace
	if _, report := trace.observe(start, remaining, false, 1048576); report {
		t.Fatal("first fragment must not be reported")
	}
	if _, report := trace.observe(start.Add(time.Second), remaining, false, 1048576); report {
		t.Fatal("ordinary fragmentation must not be reported")
	}
	metadata, report := trace.observe(start.Add(3*time.Second), remaining, false, 1048576)
	if !report || metadata.pendingBytes != 40 || !metadata.hasChannelHeader || metadata.classification != 1 || metadata.opcode != 1518 || metadata.declaredLength != 1024 {
		t.Fatalf("stalled header metadata = %+v, report = %v", metadata, report)
	}
	if !bytes.Equal(remaining, before) {
		t.Fatal("trace changed the input stream")
	}
	trace.observe(start.Add(4*time.Second), nil, true, 1048576)
	if _, report := trace.observe(start.Add(6*time.Second), remaining, false, 1048576); report {
		t.Fatal("only one report is allowed per connection")
	}
}

func TestGameStreamWaitTraceResetsOnActualParserProgress(t *testing.T) {
	start := time.Unix(100, 0)
	remaining := bytes.Repeat([]byte{0x61}, 40)
	var trace gameStreamWaitTrace
	trace.observe(start, remaining, false, 1048576)
	trace.observe(start.Add(3*time.Second), remaining, true, 1048576)
	if _, report := trace.observe(start.Add(4*time.Second), remaining, false, 1048576); report {
		t.Fatal("parser progress must restart the wait interval")
	}
	metadata, report := trace.observe(start.Add(6*time.Second), remaining, false, 1048576)
	if !report || metadata.hasChannelHeader || metadata.headerShape != "unrecognized-prefix" || metadata.pendingBytes != 40 || metadata.opcode != 0 || metadata.declaredLength != 0 {
		t.Fatalf("non-header bytes must be reduced to size only: %+v, report = %v", metadata, report)
	}
}

func TestGameStreamWaitTraceEmptyBufferClearsPendingWait(t *testing.T) {
	start := time.Unix(100, 0)
	var trace gameStreamWaitTrace
	trace.observe(start, make([]byte, 20), false, 1048576)
	trace.observe(start.Add(time.Second), nil, true, 1048576)
	if _, report := trace.observe(start.Add(4*time.Second), make([]byte, 20), false, 1048576); report {
		t.Fatal("new incomplete frame needs a fresh interval")
	}
}

func TestGameStreamWaitTraceConfigurationDefaultsOff(t *testing.T) {
	t.Setenv("DNFBRIDGE_GAME_STREAM_WAIT_TRACE", "")
	if optionsFromConfig(config.ServiceConfig{}).gameStreamWaitTrace {
		t.Fatal("diagnostic trace must default off")
	}
	t.Setenv("DNFBRIDGE_GAME_STREAM_WAIT_TRACE", "true")
	if !optionsFromConfig(config.ServiceConfig{}).gameStreamWaitTrace {
		t.Fatal("explicit diagnostic flag was not loaded")
	}
}
