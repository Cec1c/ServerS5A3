package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func newGuardianGemUniquenessService(t *testing.T) (*Service, dnfrepo.Group) {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "100380017 `flag/test_medal.equ`\n",
		"stackable/stackable.lst": "" +
			"90002 `flaggem/physical_defense_grade2.stk`\n" +
			"90003 `flaggem/physical_defense_grade3.stk`\n" +
			"90012 `flaggem/magical_defense_grade2.stk`\n",
		"stackable/flaggem/physical_defense_grade2.stk": guardianGemUniquenessPVF(2, "equipment physical defense", "180 180"),
		"stackable/flaggem/physical_defense_grade3.stk": guardianGemUniquenessPVF(3, "equipment physical defense", "450 450"),
		"stackable/flaggem/magical_defense_grade2.stk":  guardianGemUniquenessPVF(2, "equipment magical defense", "180 180"),
		"equipment/flag/test_medal.equ": "" +
			"\n[equipment type]\n`[flag]`\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("new guardian gem uniqueness catalog: %v", err)
	}
	repositories := testRepositoryGroup()
	return &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		pvfItemCatalog:     catalog,
	}, repositories
}

func guardianGemUniquenessPVF(grade int, family, values string) string {
	return "" +
		"\n[name]\n`uniqueness test gem`\n" +
		"\n[explain]\n`相同属性无法重复装备`\n" +
		"\n[grade]\n" + strconv.Itoa(grade) + "\n" +
		"\n[stackable type]\n`[flag gem]` 0\n" +
		"\n[enchant]\n[" + family + "]\n" + values + "\n[/enchant]\n"
}

func seedGuardianGemUniquenessTarget(
	t *testing.T,
	repositories dnfrepo.Group,
	socketValues [currentGuardianGemSocketCount]uint16,
	sourceItemID uint32,
	sourceCount int64,
) []byte {
	t.Helper()
	raw := currentGuardianGemTestRaw(32, 100380017, 1)
	for socket, value := range socketValues {
		offset := currentGuardianGemRawSocketOffset + socket*currentGuardianGemRawSocketWidth
		binary.LittleEndian.PutUint16(raw[offset:offset+currentGuardianGemRawSocketWidth], value)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			"38:49": {ItemID: int64(sourceItemID), Count: sourceCount},
		},
	}); err != nil {
		t.Fatalf("save guardian gem source: %v", err)
	}
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: "77",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"32": {SlotIndex: 32, ItemID: 100380017, RawEntry: raw},
		},
	}); err != nil {
		t.Fatalf("save guardian gem medal: %v", err)
	}
	return raw
}

func loadGuardianGemUniquenessMedal(t *testing.T, repositories dnfrepo.Group) []byte {
	t.Helper()
	equipment, found, err := repositories.Equipment.Load(context.Background(), "77")
	if err != nil || !found {
		t.Fatalf("load guardian gem medal found=%t err=%v", found, err)
	}
	return append([]byte(nil), equipment.Entries["32"].RawEntry...)
}

func TestCurrentGuardianGemCommitRejectsSameGemInAnotherSocketWithoutConsuming(t *testing.T) {
	service, repositories := newGuardianGemUniquenessService(t)
	before := seedGuardianGemUniquenessTarget(t, repositories, [currentGuardianGemSocketCount]uint16{3}, 90002, 1)

	_, err := service.commitCurrentGuardianGemUse(
		&gameSession{selectedCharacterID: 77},
		currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90002, SocketIndex: 1},
	)
	if err == nil || !strings.Contains(err.Error(), "attribute is already equipped") {
		t.Fatalf("same guardian gem in another socket error=%v, want duplicate-attribute rejection", err)
	}
	if got := mustLoadCurrentSocketInventory(t, repositories, "77").Slots["38:49"].Count; got != 1 {
		t.Fatalf("rejected duplicate consumed source count=%d, want=1", got)
	}
	if after := loadGuardianGemUniquenessMedal(t, repositories); !bytes.Equal(after, before) {
		t.Fatalf("rejected duplicate changed medal raw before=%x after=%x", before, after)
	}
}

func TestCurrentGuardianGemCommitRejectsSameAttributeDifferentGradeWithoutConsuming(t *testing.T) {
	service, repositories := newGuardianGemUniquenessService(t)
	before := seedGuardianGemUniquenessTarget(t, repositories, [currentGuardianGemSocketCount]uint16{3}, 90003, 1)

	_, err := service.commitCurrentGuardianGemUse(
		&gameSession{selectedCharacterID: 77},
		currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90003, SocketIndex: 1},
	)
	if err == nil || !strings.Contains(err.Error(), "attribute is already equipped") {
		t.Fatalf("same guardian attribute different grade error=%v, want duplicate-attribute rejection", err)
	}
	if got := mustLoadCurrentSocketInventory(t, repositories, "77").Slots["38:49"].Count; got != 1 {
		t.Fatalf("rejected same-attribute gem consumed source count=%d, want=1", got)
	}
	if after := loadGuardianGemUniquenessMedal(t, repositories); !bytes.Equal(after, before) {
		t.Fatalf("rejected same-attribute gem changed medal raw before=%x after=%x", before, after)
	}
}

func TestCurrentGuardianGemCommitAllowsDifferentAttributeFamily(t *testing.T) {
	service, repositories := newGuardianGemUniquenessService(t)
	seedGuardianGemUniquenessTarget(t, repositories, [currentGuardianGemSocketCount]uint16{3}, 90012, 1)

	_, err := service.commitCurrentGuardianGemUse(
		&gameSession{selectedCharacterID: 77},
		currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90012, SocketIndex: 1},
	)
	if err != nil {
		t.Fatalf("different guardian attribute family: %v", err)
	}
	inventory := mustLoadCurrentSocketInventory(t, repositories, "77")
	if stack, found := inventory.Slots["38:49"]; found && stack.Count != 0 {
		t.Fatalf("different-family source count=%d, want consumed", stack.Count)
	}
	raw := loadGuardianGemUniquenessMedal(t, repositories)
	if got := binary.LittleEndian.Uint16(raw[101:103]); got != 3 {
		t.Fatalf("physical defense socket changed=%d", got)
	}
	if got := binary.LittleEndian.Uint16(raw[103:105]); got != 13 {
		t.Fatalf("magical defense socket=%d, want item 90012 delta 13 raw=%x", got, raw[101:106])
	}
}

func TestCurrentGuardianGemCommitAllowsReplacingSameSocket(t *testing.T) {
	service, repositories := newGuardianGemUniquenessService(t)
	seedGuardianGemUniquenessTarget(t, repositories, [currentGuardianGemSocketCount]uint16{3}, 90002, 1)

	_, err := service.commitCurrentGuardianGemUse(
		&gameSession{selectedCharacterID: 77},
		currentGuardianGemUseRequest{TargetMedalItemID: 100380017, GuardianGemSourceSlot: 49, GuardianGemItemID: 90002, SocketIndex: 0},
	)
	if err != nil {
		t.Fatalf("replace same guardian gem socket: %v", err)
	}
	inventory := mustLoadCurrentSocketInventory(t, repositories, "77")
	if stack, found := inventory.Slots["38:49"]; found && stack.Count != 0 {
		t.Fatalf("same-socket replacement source count=%d, want consumed", stack.Count)
	}
	raw := loadGuardianGemUniquenessMedal(t, repositories)
	if got := binary.LittleEndian.Uint16(raw[101:103]); got != 3 {
		t.Fatalf("same-socket replacement raw=%d, want 3", got)
	}
}

func TestCurrentGuardianGemHandlerRejectsDuplicateAttributeWithoutConsumption(t *testing.T) {
	service, repositories := newGuardianGemUniquenessService(t)
	before := seedGuardianGemUniquenessTarget(t, repositories, [currentGuardianGemSocketCount]uint16{3}, 90002, 1)
	connection := &bufferConn{}
	session := &gameSession{selectedCharacterID: 77, connID: "guardian-gem-duplicate-test", conn: connection}
	body := make([]byte, currentGuardianGemUseRequestWireSize)
	binary.LittleEndian.PutUint32(body[0:4], 100380017)
	binary.LittleEndian.PutUint16(body[4:6], 49)
	binary.LittleEndian.PutUint32(body[6:10], 90002)
	body[10] = 1

	if err := service.handleCurrentGuardianGemUse(session, body); err != nil {
		t.Fatalf("handle duplicate guardian gem: %v", err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("rejected duplicate emitted refresh packets: %x", connection.write.Bytes())
	}
	if got := mustLoadCurrentSocketInventory(t, repositories, "77").Slots["38:49"].Count; got != 1 {
		t.Fatalf("handler duplicate consumed source count=%d, want=1", got)
	}
	if after := loadGuardianGemUniquenessMedal(t, repositories); !bytes.Equal(after, before) {
		t.Fatalf("handler duplicate changed medal raw before=%x after=%x", before, after)
	}
}
