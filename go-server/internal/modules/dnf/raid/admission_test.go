package raid

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type admissionSource map[string]string

func (s admissionSource) ReadText(path string) (string, error) {
	text, ok := s[path]
	if !ok {
		return "", fmt.Errorf("missing %s", path)
	}
	return text, nil
}

func TestAdmissionRuleUsesPVFCostAndRejectsUnsupportedShape(t *testing.T) {
	source := admissionSource{"raid.etc": "[ENTRY COST]\n123 2\n[WAITING ROOM]\n19 2\n[RAID START MINIMUM MEMBER]\n10\n[SINGLE RAID START MINIUM MEMBER]\n1\n"}
	rule, err := LoadAdmissionRule(source, "raid.etc")
	if err != nil || rule.ItemID != 123 || rule.Count != 2 || rule.MinimumMembers != 10 || rule.SoloMinimumMembers != 1 {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	source["raid.etc"] = strings.Replace(source["raid.etc"], "123 2\n", "123 2 456 3\n", 1)
	if _, err := LoadAdmissionRule(source, "raid.etc"); err == nil {
		t.Fatal("unsupported multi-item cost was silently shortened")
	}
}

func TestEntryCostStatusMatchesCurrentNativeByteAndShortReaders(t *testing.T) {
	got := BuildEntryCostStatusBody([]EntryCostStatus{{CharacterID: 6, Ready: true, OwnedCount: 3}, {CharacterID: 1, Ready: false, OwnedCount: 0}})
	want := []byte{2, 0, 0, 0, 6, 0, 1, 3, 0, 1, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("body=%x want=%x", got, want)
	}
}
