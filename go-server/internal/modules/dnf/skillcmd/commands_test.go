package skillcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// Reproduces the operator report "技能操作指令无法更改": the live op331 handler
// used to be plan-only (`response_allowed=false`, nothing persisted). This test
// pins the two halves of the fix: the per-skill command vector is stored, and
// the pending request is answered.
func TestOwnerApplySkillCommandsPersistsAndMerges(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	command := NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{
		DeclaredCount: 1,
		Records:       []SkillCommandRecord{{SkillID: 46, CommandBytes: []byte{0x03, 0x04}}},
	})
	result, err := owner.ApplySkillCommands(ctx, command)
	if err != nil {
		t.Fatalf("ApplySkillCommands() error = %v", err)
	}
	if result.CharacterID != "31" || result.Total != 1 || len(result.Applied) != 1 || result.Applied[0].SkillID != 46 {
		t.Fatalf("result = %+v", result)
	}
	settings, found, err := LoadSkillCommands(ctx, repos, "31")
	if err != nil || !found {
		t.Fatalf("LoadSkillCommands found=%t err=%v", found, err)
	}
	if string(settings.Commands[46]) != string([]byte{0x03, 0x04}) {
		t.Fatalf("commands = %+v", settings.Commands)
	}

	// A second customization for the same skill replaces the vector instead of
	// growing the store.
	command = NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{
		DeclaredCount: 1,
		Records:       []SkillCommandRecord{{SkillID: 46, CommandBytes: []byte{0x01, 0x08}}},
	})
	result, err = owner.ApplySkillCommands(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("total after merge = %d, want 1", result.Total)
	}
	settings, _, _ = LoadSkillCommands(ctx, repos, "31")
	if string(settings.Commands[46]) != string([]byte{0x01, 0x08}) {
		t.Fatalf("merged commands = %+v", settings.Commands)
	}
}

func TestOwnerApplySkillCommandsRejectsUnlearnedTargets(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	command := NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{
		DeclaredCount: 1,
		Records:       []SkillCommandRecord{{SkillID: 47, CommandBytes: []byte{0x04}}},
	})
	if _, err := owner.ApplySkillCommands(ctx, command); !errors.Is(err, ErrSkillCommandUnlearned) {
		t.Fatalf("error = %v, want %v", err, ErrSkillCommandUnlearned)
	}
	if _, found, err := LoadSkillCommands(ctx, repos, "31"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("rejected command still created a settings row")
	}
	empty := NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{})
	if _, err := owner.ApplySkillCommands(ctx, empty); !errors.Is(err, ErrSkillCommandEmpty) {
		t.Fatalf("empty error = %v, want %v", err, ErrSkillCommandEmpty)
	}
	tooLong := NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{
		DeclaredCount: 1,
		Records: []SkillCommandRecord{{
			SkillID:      46,
			CommandBytes: make([]byte, maxSkillCommandBytes+1),
		}},
	})
	if _, err := owner.ApplySkillCommands(ctx, tooLong); !errors.Is(err, ErrSkillCommandTooLong) {
		t.Fatalf("long error = %v, want %v", err, ErrSkillCommandTooLong)
	}
}

// op332 ("全部默认") clears the persisted vector.
func TestOwnerClearSkillCommandsRemovesEveryRecord(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	command := NewSkillCommand(alignedRequest("acc-skill", 31), SkillCommandRequest{
		DeclaredCount: 1,
		Records:       []SkillCommandRecord{{SkillID: 46, CommandBytes: []byte{0x03, 0x04}}},
	})
	if _, err := owner.ApplySkillCommands(ctx, command); err != nil {
		t.Fatal(err)
	}
	cleared, err := owner.ClearSkillCommands(ctx, NewSkillCommandDefaultsCommand(alignedRequest("acc-skill", 31)))
	if err != nil {
		t.Fatalf("ClearSkillCommands() error = %v", err)
	}
	if cleared.CharacterID != "31" || cleared.Cleared != 1 {
		t.Fatalf("cleared = %+v", cleared)
	}
	settings, found, err := LoadSkillCommands(ctx, repos, "31")
	if err != nil {
		t.Fatal(err)
	}
	if found && len(settings.Commands) != 0 {
		t.Fatalf("commands survived the clear: %+v", settings.Commands)
	}
}

func TestHandlerSkillCommandCustomizingAcksWithEchoedBody(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	body := []byte{0x01, 0x2e, 0x00, 0x01, 0x04}
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSkillCommandCustomizing),
		Body:                body,
		AccountID:           "acc-skill",
		SelectedCharacterID: 31,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v, want one op331 ACK", got)
	}
	if got.Operation != "skill_command_customizing" {
		t.Fatalf("operation = %q", got.Operation)
	}
	response := got.UpperResponses[0]
	want := append([]byte{1}, body...)
	if response.MsgID != uint16(dnfenum.CmdPacketSkillCommandCustomizing) ||
		response.Classification != dnfproto.DefaultChannelClassification ||
		!response.AllowCodec ||
		string(response.Body) != string(want) {
		t.Fatalf("response = %+v body=%x want %x", response, response.Body, want)
	}
	settings, found, err := LoadSkillCommands(ctx, repos, "31")
	if err != nil || !found {
		t.Fatalf("persisted found=%t err=%v", found, err)
	}
	if string(settings.Commands[46]) != string([]byte{0x04}) {
		t.Fatalf("persisted commands = %+v", settings.Commands)
	}
}

func TestHandlerSkillCommandAllDefaultAcksOneByte(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSkillCommandAllDefault),
		AccountID:           "acc-skill",
		SelectedCharacterID: 31,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v", got)
	}
	if got.Operation != "skill_command_all_default" || string(got.UpperResponses[0].Body) != string([]byte{1}) {
		t.Fatalf("response = %+v", got.UpperResponses[0])
	}
	if _, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSkillCommandAllDefault),
		Body:                []byte{1},
		AccountID:           "acc-skill",
		SelectedCharacterID: 31,
		Repositories:        repos,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerSkillCommandCustomizingBlocksUnparsableBody(t *testing.T) {
	ctx := context.Background()
	repos := seededSkillOwnerRepositories(t, ctx)
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketSkillCommandCustomizing),
		Body:                []byte{0x02, 0x2e, 0x00, 0x40},
		AccountID:           "acc-skill",
		SelectedCharacterID: 31,
		Repositories:        repos,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Handled || got.ResponseAllowed || len(got.UpperResponses) != 0 {
		t.Fatalf("result = %+v, want a blocked ACK", got)
	}
}

func TestSortedSkillCommandIDsIsDeterministic(t *testing.T) {
	got := SortedSkillCommandIDs(map[uint16][]byte{79: nil, 25: nil, 310: nil})
	if !reflect.DeepEqual(got, []uint16{25, 79, 310}) {
		t.Fatalf("ids = %v", got)
	}
}
