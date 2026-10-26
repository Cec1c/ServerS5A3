package dnfbridge

import (
	"errors"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"testing"
)

func TestJobChangeTrialAcceptsCapturedDirectEntryOnly(t *testing.T) {
	body := mustHexBytes(t, "e61b00000000000001ffff00000000000000000000")
	request, err := dungeoncmd.DecodeSelectDungeonRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCurrentDungeonEntryRequest(request); err != nil {
		t.Fatalf("现场7142体验请求被拒绝：%v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*dungeoncmd.SelectDungeonRequest)
	}{
		{"neighbor", func(r *dungeoncmd.SelectDungeonRequest) { r.DungeonID = 7143 }},
		{"token", func(r *dungeoncmd.SelectDungeonRequest) { r.RuntimeToken = 0 }},
		{"runtime_state", func(r *dungeoncmd.SelectDungeonRequest) { r.RuntimeState = 2 }},
		{"reserved", func(r *dungeoncmd.SelectDungeonRequest) { r.Reserved = 1 }},
		{"difficulty", func(r *dungeoncmd.SelectDungeonRequest) { r.Difficulty = 1 }},
		{"entry_option", func(r *dungeoncmd.SelectDungeonRequest) { r.EntryOption = 1 }},
		{"selection", func(r *dungeoncmd.SelectDungeonRequest) { r.SelectionMode = 1 }},
		{"party", func(r *dungeoncmd.SelectDungeonRequest) { r.PartyState = 1 }},
		{"unknown_owner", func(r *dungeoncmd.SelectDungeonRequest) { r.LeaderObjectKey = 1 }},
		{"special", func(r *dungeoncmd.SelectDungeonRequest) { r.SpecialMode = 1 }},
		{"tail", func(r *dungeoncmd.SelectDungeonRequest) { r.OpaqueTail = []byte{1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			tc.mutate(&r)
			if err := validateCurrentDungeonEntryRequest(r); !errors.Is(err, errDungeonEntryModeUnsupported) {
				t.Fatalf("非现场体验形状应拒绝：%+v error=%v", r, err)
			}
		})
	}
}
