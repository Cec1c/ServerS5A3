package inventory

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestEquipmentUpgradeNoticeTicketsShareThresholdAfterCommit(t *testing.T) {
	for _, mode := range []string{"reinforce", "amplify"} {
		for _, tc := range []struct {
			old                       byte
			target, weight            int64
			policy, policyError, want bool
		}{
			{2, 10, 100000, true, false, false}, {2, 12, 100000, true, false, true},
			{12, 13, 0, true, false, true}, {2, 12, 100000, false, false, false}, {2, 12, 100000, true, true, false},
		} {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			extra := map[string]string{}
			if mode == "amplify" {
				extra["amplify_type"], extra["amplify_value"] = "1", "5"
			}
			saveUpgradeTicketFixture(t, ctx, repos, extra, tc.old, 2)
			body := upgradeTicketRequestBody(9, 700, 121)
			if mode == "amplify" {
				binary.LittleEndian.PutUint16(body, 1)
			}
			req := alignedcmd.Request{Opcode: 0x50, Body: body, AccountID: "acc", SelectedCharacterID: 77, Repositories: repos,
				UpgradeTicketResolver: staticUpgradeTicketResolver(alignedcmd.UpgradeTicketResolution{TicketMode: mode, TargetLevel: tc.target, SuccessWeight: tc.weight, TargetKind: "equipment"})}
			if tc.policy {
				req.UpgradePolicyResolver = func(string, int64, int) (alignedcmd.UpgradePolicyResolution, error) {
					if tc.policyError {
						return alignedcmd.UpgradePolicyResolution{}, errors.New("模拟播报配置不可用")
					}
					return alignedcmd.UpgradePolicyResolution{NoticeLevel: 12}, nil
				}
			}
			result, err := NewHandler().Handle(ctx, req)
			if err != nil || (result.EquipmentUpgradeNotice != nil) != tc.want || result.UpperResponses[0].Body[0] != 1 {
				t.Fatalf("券播报 mode=%s old=%d target=%d want=%t result=%+v err=%v", mode, tc.old, tc.target, tc.want, result, err)
			}
			bag := loadTestInventory(t, ctx, repos, "77")
			if bag.Slots["0:121"].Count != 1 {
				t.Fatal("播报改变券的消耗次数")
			}
			if tc.want && (result.EquipmentUpgradeNotice.Kind != mode || len(result.EquipmentUpgradeNotice.Body) != 10) {
				t.Fatal("券播报模式或原生尾部错误")
			}
		}
	}
}

func TestEquipmentUpgradeNoticeRulesAndSnapshot(t *testing.T) {
	stack := dnfrepo.ItemStack{ItemID: 700, Count: 1}
	for _, tc := range []struct {
		old, next byte
		success   bool
		threshold int
		want      bool
	}{
		{10, 11, true, 12, false}, {11, 12, true, 12, true}, {12, 0, false, 12, true}, {11, 0, false, 12, false}, {12, 13, true, -1, false},
	} {
		cmd := Command{SelectedCharacterID: 19, Mode: "reinforce", UpgradeNoticeLevel: tc.threshold}
		result := ownerAppliedUpgradeResult(context.Background(), nil, 0x50, cmd, UpgradeResult{Success: true, Mode: "reinforce", OldLevel: tc.old, NewLevel: tc.next, UpgradeSucceeded: tc.success, TargetItemTemplateID: 700, TargetNoticeStack: stack})
		if (result.EquipmentUpgradeNotice != nil) != tc.want {
			t.Fatalf("强化规则 old=%d next=%d success=%t", tc.old, tc.next, tc.success)
		}
		if tc.want && (len(result.EquipmentUpgradeNotice.Body) != 10 || result.EquipmentUpgradeNotice.Body[9] != 0) {
			t.Fatal("缺少原生动态属性计数")
		}
	}
	for _, tc := range []struct {
		old, next             byte
		success, ticket, want bool
	}{
		{3, 4, true, false, false}, {4, 5, true, false, true}, {5, 5, false, false, true}, {4, 4, false, false, false},
		{4, 5, true, true, true}, {5, 5, false, true, false},
	} {
		result := ownerAppliedSeparateUpgradeResult(Command{SelectedCharacterID: 19}, SeparateUpgradeResult{Success: true, OldLevel: tc.old, NewLevel: tc.next, Succeeded: tc.success, TicketApplied: tc.ticket, TargetItemID: 700, TargetUpdatedStack: stack})
		if (result.EquipmentUpgradeNotice != nil) != tc.want {
			t.Fatalf("锻造规则 old=%d next=%d success=%t ticket=%t", tc.old, tc.next, tc.success, tc.ticket)
		}
		if tc.want && (len(result.EquipmentUpgradeNotice.Body) != 11 || result.EquipmentUpgradeNotice.Body[0] != 14 || result.EquipmentUpgradeNotice.Body[9] != tc.next) {
			t.Fatal("锻造原生字段不完整")
		}
	}
	result := ownerAppliedSeparateUpgradeResult(Command{}, SeparateUpgradeResult{Rejected: true})
	if result.EquipmentUpgradeNotice != nil {
		t.Fatal("校验拒绝产生播报")
	}
}

func TestEquipmentUpgradeNoticePreservesDynamicPropertiesAndRejectsMalformed(t *testing.T) {
	raw := make([]byte, currentItemListEntrySize)
	raw[randomOptionCountOffset] = 1
	raw[randomOptionTypeOffset], raw[randomOptionValue1Offset], raw[randomOptionValue2Offset] = 2, 9, 10
	raw[randomOptionStateOffset], raw[randomOptionChangedIndexOffset] = 3, 0xff
	stack := dnfrepo.ItemStack{ItemID: 700, Count: 1, RawEntry: raw}
	body, err := buildEquipmentUpgradeAnnouncementBody(1, false, 19, 700, 12, 0, stack)
	if err != nil || len(body) != 15 || binary.LittleEndian.Uint16(body[2:4]) != 19 || body[9] != 1 || body[10] != 2 || body[11] != 9 || body[12] != 10 || body[14] != 0xff {
		t.Fatalf("动态属性播报=%x err=%v", body, err)
	}
	stack.RawEntry[randomOptionCountOffset] = 4
	if _, err := buildEquipmentUpgradeAnnouncementBody(1, true, 19, 700, 12, 0, stack); err == nil {
		t.Fatal("接受超量动态属性")
	}
	stack.RawEntry = nil
	if _, err := buildEquipmentUpgradeAnnouncementBody(14, true, 19, 701, 12, 5, stack); err == nil {
		t.Fatal("接受错误装备快照")
	}
	raw = make([]byte, currentItemListEntrySize)
	raw[randomOptionCountOffset], raw[randomOptionChangedIndexOffset] = 3, 1
	copy(raw[randomOptionCandidateOffset:randomOptionCandidateOffset+4], []byte{0, 2, 9, 10})
	stack.RawEntry = raw
	if body, err := buildEquipmentUpgradeAnnouncementBody(14, true, 19, 700, 12, 5, stack); err != nil || len(body) != 26 || body[22] != 0 || body[23] != 2 || body[25] != 10 {
		t.Fatalf("三属性及待选属性尾部=%x err=%v", body, err)
	}
}
