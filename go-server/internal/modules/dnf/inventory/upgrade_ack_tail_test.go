package inventory

import (
	"bytes"
	"encoding/binary"
	"testing"
)

var upgradeAckNeutralTail = []byte{0xff, 0xff, 0x00, 0x00, 0x00, 0x00}

func requireUpgradeItemAckOutcome(t *testing.T, body []byte, oldLevel, resultCode, newLevel byte) {
	t.Helper()
	if len(body) < 15 {
		t.Fatalf("upgrade ACK is only %d bytes: %x", len(body), body)
	}
	if got := body[11]; got != oldLevel {
		t.Fatalf("old level = %d, want %d (ACK=%x)", got, oldLevel, body)
	}
	if got := body[12]; got != resultCode {
		t.Fatalf("result code = %d, want %d (ACK=%x)", got, resultCode, body)
	}
	if got := body[13]; got != newLevel {
		t.Fatalf("new level = %d, want %d (ACK=%x)", got, newLevel, body)
	}
}

func requireUpgradeAckTail(t *testing.T, body, want []byte) {
	t.Helper()
	if len(body) < len(want) {
		t.Fatalf("upgrade ACK is only %d bytes: %x", len(body), body)
	}
	if got := body[len(body)-len(want):]; !bytes.Equal(got, want) {
		t.Fatalf("upgrade ACK tail = %x, want %x (ACK=%x)", got, want, body)
	}
}

func TestUpgradeItemSuccessAckAlwaysCarriesNeutralMaterialTail(t *testing.T) {
	testCases := []struct {
		name      string
		mode      string
		oldLevel  byte
		result    byte
		newLevel  byte
		protected bool
	}{
		{name: "success", mode: "reinforce", oldLevel: 7, result: upgradeResultCodeSuccess, newLevel: 8},
		{name: "failure_retains_level", mode: "amplify", oldLevel: 11, result: upgradeResultCodeFailNoChange, newLevel: 11},
		{name: "downgrade", mode: "reinforce", oldLevel: 14, result: upgradeResultCodeDowngrade, newLevel: 10},
		{name: "protected_downgrade", mode: "reinforce", oldLevel: 15, result: upgradeResultCodeDowngrade, newLevel: 14, protected: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := UpgradeResult{
				Mode:                      testCase.mode,
				Success:                   true,
				TargetSlotIndex:           9,
				MaterialSlotIndex:         -1,
				OptionalTicketSlotIndex:   -1,
				OldLevel:                  testCase.oldLevel,
				NewLevel:                  testCase.newLevel,
				ResultCode:                testCase.result,
				ProtectTicketScanned:      testCase.protected,
				ProtectTicketConsumed:     testCase.protected,
				ConversionMaterialUpdated: false,
			}
			body := buildUpgradeItemSuccessAck(result)
			if len(body) != 25 {
				t.Fatalf("upgrade ACK length = %d, want 25 (ACK=%x)", len(body), body)
			}
			requireUpgradeItemAckOutcome(t, body, testCase.oldLevel, testCase.result, testCase.newLevel)
			requireUpgradeAckTail(t, body, upgradeAckNeutralTail)
		})
	}
}

func TestUpgradeItemDestroyAckPreservesRewardBeforeNeutralTail(t *testing.T) {
	t.Run("without reward", func(t *testing.T) {
		result := UpgradeResult{
			Mode:              "reinforce",
			Success:           true,
			TargetSlotIndex:   9,
			MaterialSlotIndex: -1,
			OldLevel:          13,
			NewLevel:          0,
			ResultCode:        upgradeResultCodeDestroy,
		}
		body := buildUpgradeItemSuccessAck(result)
		if len(body) != 26 {
			t.Fatalf("destroy ACK length = %d, want 26 (ACK=%x)", len(body), body)
		}
		requireUpgradeItemAckOutcome(t, body, result.OldLevel, result.ResultCode, result.NewLevel)
		if body[19] != 0 {
			t.Fatalf("destroy reward marker = %d, want 0 (ACK=%x)", body[19], body)
		}
		requireUpgradeAckTail(t, body, upgradeAckNeutralTail)
	})

	t.Run("with reward", func(t *testing.T) {
		result := UpgradeResult{
			Mode:               "reinforce",
			Success:            true,
			TargetSlotIndex:    9,
			MaterialSlotIndex:  -1,
			OldLevel:           13,
			NewLevel:           0,
			ResultCode:         upgradeResultCodeDestroy,
			DestroyBonusSlot:   121,
			DestroyBonusItemID: 3243,
			DestroyBonusCount:  9,
		}
		body := buildUpgradeItemSuccessAck(result)
		if len(body) != 36 {
			t.Fatalf("destroy-with-reward ACK length = %d, want 36 (ACK=%x)", len(body), body)
		}
		requireUpgradeItemAckOutcome(t, body, result.OldLevel, result.ResultCode, result.NewLevel)
		reward := body[19 : len(body)-len(upgradeAckNeutralTail)]
		wantReward := make([]byte, 11)
		wantReward[0] = 1
		binary.LittleEndian.PutUint16(wantReward[1:3], uint16(result.DestroyBonusSlot))
		binary.LittleEndian.PutUint32(wantReward[3:7], uint32(result.DestroyBonusItemID))
		binary.LittleEndian.PutUint32(wantReward[7:11], uint32(result.DestroyBonusCount))
		if !bytes.Equal(reward, wantReward) {
			t.Fatalf("destroy reward = %x, want %x (ACK=%x)", reward, wantReward, body)
		}
		requireUpgradeAckTail(t, body, upgradeAckNeutralTail)
	})
}

func TestUpgradeItemSuccessAckCarriesConversionMaterialTail(t *testing.T) {
	testCases := []struct {
		name      string
		remaining int64
		wantTail  []byte
	}{
		{name: "remaining material", remaining: 4, wantTail: []byte{0x87, 0x00, 0x04, 0x00, 0x00, 0x00}},
		{name: "exhausted material", remaining: 0, wantTail: []byte{0x87, 0x00, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := UpgradeResult{
				Mode:                             "reinforce",
				Success:                          true,
				TargetSlotIndex:                  9,
				MaterialSlotIndex:                -1,
				OldLevel:                         7,
				NewLevel:                         8,
				ResultCode:                       upgradeResultCodeSuccess,
				ConversionMaterialUpdated:        true,
				ConversionMaterialSlot:           135,
				ConversionMaterialRemainingCount: testCase.remaining,
			}
			body := buildUpgradeItemSuccessAck(result)
			if len(body) != 25 {
				t.Fatalf("conversion ACK length = %d, want 25 (ACK=%x)", len(body), body)
			}
			requireUpgradeItemAckOutcome(t, body, result.OldLevel, result.ResultCode, result.NewLevel)
			requireUpgradeAckTail(t, body, testCase.wantTail)
		})
	}
}

func TestUpgradeTicketSuccessAckCarriesNeutralMaterialTail(t *testing.T) {
	result := UpgradeTicketResult{
		Mode:                        "amplify",
		Success:                     true,
		TicketResolved:              true,
		TargetSlotIndex:             9,
		MaterialSlotIndex:           135,
		OldLevel:                    17,
		NewLevel:                    18,
		ResultCode:                  upgradeTicketResultCodeSuccess,
		MaterialRemainingStackCount: 0,
	}
	body := buildUpgradeTicketSuccessAck(result)
	if len(body) != 25 {
		t.Fatalf("ticket ACK length = %d, want 25 (ACK=%x)", len(body), body)
	}
	if body[0] != 1 || body[1] != 1 {
		t.Fatalf("ticket ACK header = %x, want success flag and amplify mode", body[:2])
	}
	if got := int16(binary.LittleEndian.Uint16(body[2:4])); got != result.MaterialSlotIndex {
		t.Fatalf("ticket material slot = %d, want %d", got, result.MaterialSlotIndex)
	}
	if got := int16(binary.LittleEndian.Uint16(body[15:17])); got != result.TargetSlotIndex {
		t.Fatalf("ticket target slot = %d, want %d", got, result.TargetSlotIndex)
	}
	requireUpgradeItemAckOutcome(t, body, result.OldLevel, result.ResultCode, result.NewLevel)
	requireUpgradeAckTail(t, body, upgradeAckNeutralTail)
}
