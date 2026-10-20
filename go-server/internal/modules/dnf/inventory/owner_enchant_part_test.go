package inventory

import (
	"context"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 部位限制（op272）回归：宝珠能不能打某个部位，只由运行时 PVF 的
// 「卡片 [string data] 部位表」决定（现场口径：「同颗宝珠无法同时打宠物和装备，
// 宝珠是有部位限制的」）。
//
// 允许的部位 → 成功且消耗 1；不允许的部位 → 明确拒绝（0x0110 error=0x13）、
// 不消耗宝珠、不写库、日志 reason=bead_part_mismatch。宠物与装备两个方向都覆盖，
// 缺部位证据（部位表为空 / 目标没有部位）同样按 fail-closed 拒绝。
func TestOwnerEnchantPartRestriction(t *testing.T) {
	const beadCount = int64(2)
	tests := []struct {
		name        string
		resolution  alignedcmd.EnchantBeadResolution
		targetPet   bool
		wantSuccess bool
		wantReason  string
	}{
		{
			name: "宠物宝珠打宠物：部位表 [creature] 命中，成功并消耗 1",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            10008663,
				TargetWhitelist:       []int64{400990168},
				AllowedEquipmentTypes: []string{"[creature]"},
				TargetEquipmentType:   "[creature]",
				TargetKind:            "equipment",
			},
			targetPet:   true,
			wantSuccess: true,
		},
		{
			name: "宠物宝珠打装备：部位表只有 [creature]，目标 [coat] 必须拒绝",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            10008161,
				AllowedEquipmentTypes: []string{"[creature]"},
				TargetEquipmentType:   "[coat]",
				TargetKind:            "equipment",
			},
			wantReason: enchantPartRejectionMismatch,
		},
		{
			name: "装备宝珠打宠物：部位表 [waist][shoulder][shoes] 不含 [creature]，必须拒绝",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            10002536,
				AllowedEquipmentTypes: []string{"[waist]", "[shoulder]", "[shoes]"},
				TargetEquipmentType:   "[creature]",
				TargetKind:            "equipment",
			},
			targetPet:  true,
			wantReason: enchantPartRejectionMismatch,
		},
		{
			name: "装备宝珠打允许的部位：部位表含 [coat]，成功并消耗 1",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            9001,
				AllowedEquipmentTypes: []string{"[coat]", "[pants]"},
				TargetEquipmentType:   "[coat]",
				TargetKind:            "equipment",
			},
			wantSuccess: true,
		},
		{
			name: "装备宝珠打不允许的部位：[coat] 不在 [waist][shoulder] 表里，必须拒绝",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            9001,
				AllowedEquipmentTypes: []string{"[waist]", "[shoulder]"},
				TargetEquipmentType:   "[coat]",
				TargetKind:            "equipment",
			},
			wantReason: enchantPartRejectionMismatch,
		},
		{
			name: "宝珠卡片没有部位表：没有限制也不能随便打，必须拒绝",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:          9001,
				TargetEquipmentType: "[coat]",
				TargetKind:          "equipment",
			},
			wantReason: enchantPartRejectionUnresolved,
		},
		{
			name: "目标没有部位证据（缺 [equipment type]）：必须拒绝",
			resolution: alignedcmd.EnchantBeadResolution{
				CardItemID:            9001,
				AllowedEquipmentTypes: []string{"[coat]"},
				TargetKind:            "equipment",
			},
			wantReason: enchantPartRejectionUnresolved,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			var cmd Command
			if tc.targetPet {
				savePetEnchantFixture(t, ctx, repos, beadCount)
				cmd = newEnchantTestCommand(12, 24)
				cmd.TargetListType = listTypePet
			} else {
				saveEnchantFixture(t, ctx, repos, beadCount, 0, 0)
				cmd = newEnchantTestCommand(12, 30)
			}
			owner, err := NewOwner(repos)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.Enchant(ctx, cmd, staticEnchantResolver(tc.resolution))
			if err != nil {
				t.Fatalf("Enchant error = %v", err)
			}
			loaded := loadTestInventory(t, ctx, repos, "77")
			bead := loaded.Slots[slotKey(listTypeMain, 12)]

			if tc.wantSuccess {
				if !result.Success || !result.Changed || result.PartRejection != "" {
					t.Fatalf("result = %+v, want success", result)
				}
				if bead.Count != beadCount-1 {
					t.Fatalf("bead count = %d, want %d (one consumed)", bead.Count, beadCount-1)
				}
				return
			}

			if result.Success || result.Changed || result.ErrorCode != enchantErrorInvalidTarget {
				t.Fatalf("result = %+v, want explicit 0x13 part rejection", result)
			}
			if result.PartRejection != tc.wantReason {
				t.Fatalf("part reason = %q, want %q", result.PartRejection, tc.wantReason)
			}
			if bead.Count != beadCount {
				t.Fatalf("bead count = %d, want %d (part rejection must not consume)", bead.Count, beadCount)
			}
			if tc.targetPet {
				if got := loaded.Slots[slotKey(listTypePet, 24)].Extra["value_a"]; got != "" {
					t.Fatalf("pet target written on part rejection: value_a=%q", got)
				}
			} else {
				if got := loaded.Slots[slotKey(listTypeMain, 30)].Extra["value_a"]; got != "" {
					t.Fatalf("equipment target written on part rejection: value_a=%q", got)
				}
			}
		})
	}
}

// 宠物坐标里放的不是宠物时也要拒绝，并且日志原因要和"部位表不含目标部位"区分开。
func TestOwnerEnchantPetLaneTargetMustBeCreature(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	savePetEnchantFixture(t, ctx, repos, 2)
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	cmd := newEnchantTestCommand(12, 24)
	cmd.TargetListType = listTypePet
	result, err := owner.Enchant(ctx, cmd, staticEnchantResolver(alignedcmd.EnchantBeadResolution{
		CardItemID:            10002536,
		AllowedEquipmentTypes: []string{"[shoes]", "[creature]"},
		TargetEquipmentType:   "[shoes]",
		TargetKind:            "equipment",
	}))
	if err != nil {
		t.Fatalf("Enchant error = %v", err)
	}
	if result.Success || result.PartRejection != enchantPartRejectionPetLane {
		t.Fatalf("result = %+v, want pet-lane rejection", result)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 12)].Count; got != 2 {
		t.Fatalf("bead consumed on pet-lane rejection: count=%d", got)
	}
}

// 部位限制的拒绝必须是"明确失败"：回客户端已验证的 0x0110 失败包（error=0x13），
// 日志里能直接 grep 到 bead=<id> / target_list / target_slot / reason=bead_part_mismatch。
func TestHandlerEnchantByBeadPartMismatchSendsFailureAckAndLogsReason(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveEnchantFixture(t, ctx, repos, 2, 0, 0)
	resolver := staticEnchantResolver(alignedcmd.EnchantBeadResolution{
		CardItemID:            10008161,
		AllowedEquipmentTypes: []string{"[creature]"},
		TargetEquipmentType:   "[coat]",
		TargetKind:            "equipment",
	})

	got := handleEnchantRequest(t, repos, resolver, enchantRequestBody(listTypeMain, 12, listTypeMain, 30))
	if !got.Handled || !got.ResponseAllowed || len(got.UpperResponses) != 1 {
		t.Fatalf("result = %+v, want a single failure ACK", got)
	}
	ack := got.UpperResponses[0]
	if ack.MsgID != uint16(dnfenum.CmdPacketEnchantByBead) || ack.Classification != 1 {
		t.Fatalf("ack = class%d msg0x%04X, want class1 0x0110", ack.Classification, ack.MsgID)
	}
	if string(ack.Body) != string([]byte{0, enchantErrorInvalidTarget}) {
		t.Fatalf("ack body = %x, want {0,0x13}", ack.Body)
	}
	for _, want := range []string{
		"bead=8801",
		"target_list=0",
		"target_slot=30",
		"reason=bead_part_mismatch",
		`target_part="[coat]"`,
		"bead_parts=[[creature]]",
	} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason %q missing %q", got.Reason, want)
		}
	}

	loaded := loadTestInventory(t, ctx, repos, "77")
	if gotCount := loaded.Slots[slotKey(listTypeMain, 12)].Count; gotCount != 2 {
		t.Fatalf("bead consumed on part rejection: count=%d", gotCount)
	}
	if gotValue := loaded.Slots[slotKey(listTypeMain, 30)].Extra["value_a"]; gotValue != "" {
		t.Fatalf("target written on part rejection: value_a=%q", gotValue)
	}
}
