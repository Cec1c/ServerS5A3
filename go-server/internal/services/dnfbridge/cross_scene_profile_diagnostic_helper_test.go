package dnfbridge

// Minimal test helper extracted from the private diagnostic test solely for
// the accepted cross-scene profile diagnostic checks. The unrelated raid
// diagnostic tests in that private file are excluded.

import (
	"bytes"
	"encoding/json"
	"testing"
)

func r195DiagnosticEvent(t *testing.T, logs *bytes.Buffer, kind string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] == kind {
			found = event
		}
	}
	if found == nil {
		t.Fatalf("missing %s in %s", kind, logs.String())
	}
	return found
}
