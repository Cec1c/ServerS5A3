package repository_test

import (
	"context"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestInventoryCapacitySnapshotDoesNotPersistOrAlias(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	settings := repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("1"), Values: map[string]string{"main_list_param16": "8", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}
	if err := g.Settings.Save(ctx, settings); err != nil {
		t.Fatal(err)
	}
	record := repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{"0:9": {ItemID: 100, Count: 1}}}
	if err := repo.BindInventoryContainerState(ctx, g.Settings, &record); err != nil {
		t.Fatal(err)
	}
	cloned := repo.CloneInventory(record)
	cloned.ContainerState.MainSlotCount = 16
	if record.ContainerState.MainSlotCount != 8 {
		t.Fatal("容量快照与调用方发生别名共享")
	}
	if err := repo.SaveInventoryFields(ctx, g.Inventory, cloned, repo.InventoryFieldSlots); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := g.Inventory.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContainerState != nil {
		t.Fatal("容量快照不应写入库存")
	}
	if err := repo.BindInventoryContainerState(ctx, g.Settings, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.ContainerState.MainSlotCount != 8 {
		t.Fatal("容量必须从真实扩容记录重新读取")
	}
}
