package memory

import (
	"context"
	"errors"
	"longheng.io/server/internal/modules/dnf/repository"
	platformdb "longheng.io/server/internal/platform/db"
	"reflect"
	"testing"
)

func TestMemoryAccountCharacterPetAssetUnitOfWorkCommitsFiveContainers(t *testing.T) {
	ctx := context.Background()
	repos := NewMemoryGroup()
	seedAccountCharacterPetAssetTransaction(t, ctx, repos)
	uow := repos.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)

	err := uow.WithinAccountCharacterPetAssets(ctx, "dnf:1", "77", func(
		account repository.AccountInventoryRepository,
		character repository.CharacterRepository,
		inventory repository.InventoryRepository,
		equipment repository.EquipmentRepository,
		pet repository.PetRepository,
	) error {
		return mutateAccountCharacterPetAssets(ctx, account, character, inventory, equipment, pet)
	})
	if err != nil {
		t.Fatalf("WithinAccountCharacterPetAssets() error = %v", err)
	}

	account, _, _ := repos.AccountInventory.Load(ctx, "dnf:1")
	character, _, _ := repos.Character.Load(ctx, "77")
	inventory, _, _ := repos.Inventory.Load(ctx, "77")
	equipment, _, _ := repos.Equipment.Load(ctx, "77")
	pet, _, _ := repos.Pet.Load(ctx, "77")
	if got := account.Slots["0:354"].Count; got != 4 {
		t.Fatalf("account material count = %d, want 4", got)
	}
	if got := character.Stats["gold"]; got != 125 {
		t.Fatalf("gold = %d, want 125", got)
	}
	if got := inventory.Slots["0:9"].Count; got != 2 {
		t.Fatalf("inventory count = %d, want 2", got)
	}
	if got := equipment.Entries["24"]; got.ItemID != 63000 || got.SlotIndex != 24 {
		t.Fatalf("equipment entry = %+v", got)
	}
	if pet.EquippedKey != "9001" || pet.Entries["9001"].Exp != 7 {
		t.Fatalf("pet record = %+v", pet)
	}
}

func TestMemoryAccountCharacterPetAssetUnitOfWorkRestoresEveryAttemptedContainer(t *testing.T) {
	for _, mode := range []string{"callback_error", "cancel", "pet_error", "pet_panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repos := NewMemoryGroup()
			seedAccountCharacterPetAssetTransaction(t, ctx, repos)
			before := loadAccountCharacterPetAssetSnapshot(t, ctx, repos)
			uow := repos.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			callbackErr := errors.New("reject pet asset transaction")
			petSaveErr := errors.New("late pet save failed")

			switch mode {
			case "cancel":
				uow.pet = &accountPetCancelAfterSave{
					PetRepository: repos.Pet,
					cancel:        cancel,
				}
			case "pet_error", "pet_panic":
				uow.pet = &accountPetFaultStore{
					PetRepository: repos.Pet,
					mode:          mode,
					saveErr:       petSaveErr,
				}
			}

			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err = uow.WithinAccountCharacterPetAssets(ctx, "dnf:1", "77", func(
					account repository.AccountInventoryRepository,
					character repository.CharacterRepository,
					inventory repository.InventoryRepository,
					equipment repository.EquipmentRepository,
					pet repository.PetRepository,
				) error {
					if err := mutateAccountCharacterPetAssets(ctx, account, character, inventory, equipment, pet); err != nil {
						return err
					}
					if mode == "callback_error" {
						return callbackErr
					}
					return nil
				})
			}()

			switch mode {
			case "callback_error":
				if !errors.Is(err, callbackErr) {
					t.Fatalf("error = %v, want %v", err, callbackErr)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
			case "pet_error":
				if !errors.Is(err, petSaveErr) {
					t.Fatalf("error = %v, want %v", err, petSaveErr)
				}
			case "pet_panic":
				if !panicked {
					t.Fatal("pet save panic was swallowed")
				}
			}
			assertAccountCharacterPetAssetSnapshot(t, before, loadAccountCharacterPetAssetSnapshot(t, context.Background(), repos))
		})
	}
}

func TestMemoryAccountCharacterPetAssetUnitOfWorkScopesEveryRepositoryKey(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, repository.AccountInventoryRepository, repository.CharacterRepository, repository.InventoryRepository, repository.EquipmentRepository, repository.PetRepository) error
	}{
		{
			name: "account inventory",
			run: func(ctx context.Context, account repository.AccountInventoryRepository, _ repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.PetRepository) error {
				if _, _, err := account.Load(ctx, "dnf:2"); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("account inventory accepted a foreign load key")
				}
				if err := account.Save(ctx, repository.AccountInventoryRecord{AccountID: "dnf:2"}); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("account inventory accepted a foreign save key")
				}
				return nil
			},
		},
		{
			name: "character",
			run: func(ctx context.Context, _ repository.AccountInventoryRepository, character repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.PetRepository) error {
				if _, _, err := character.Load(ctx, "78"); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("character accepted a foreign load key")
				}
				if err := character.Save(ctx, repository.CharacterRecord{CharacterID: "78"}); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("character accepted a foreign save key")
				}
				return nil
			},
		},
		{
			name: "inventory",
			run: func(ctx context.Context, _ repository.AccountInventoryRepository, _ repository.CharacterRepository, inventory repository.InventoryRepository, _ repository.EquipmentRepository, _ repository.PetRepository) error {
				if _, _, err := inventory.Load(ctx, "78"); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("inventory accepted a foreign load key")
				}
				if err := inventory.Save(ctx, repository.InventoryRecord{CharacterID: "78"}); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("inventory accepted a foreign save key")
				}
				return nil
			},
		},
		{
			name: "equipment",
			run: func(ctx context.Context, _ repository.AccountInventoryRepository, _ repository.CharacterRepository, _ repository.InventoryRepository, equipment repository.EquipmentRepository, _ repository.PetRepository) error {
				if _, _, err := equipment.Load(ctx, "78"); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("equipment accepted a foreign load key")
				}
				if err := equipment.Save(ctx, repository.EquipmentRecord{CharacterID: "78"}); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("equipment accepted a foreign save key")
				}
				return nil
			},
		},
		{
			name: "pet",
			run: func(ctx context.Context, _ repository.AccountInventoryRepository, _ repository.CharacterRepository, _ repository.InventoryRepository, _ repository.EquipmentRepository, pet repository.PetRepository) error {
				if _, _, err := pet.Load(ctx, "78"); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("pet accepted a foreign load key")
				}
				if err := pet.Save(ctx, repository.PetRecord{CharacterID: "78"}); !errors.Is(err, platformdb.ErrRecordKeyRequired) {
					return errors.New("pet accepted a foreign save key")
				}
				return nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := NewMemoryGroup()
			seedAccountCharacterPetAssetTransaction(t, ctx, repos)
			before := loadAccountCharacterPetAssetSnapshot(t, ctx, repos)
			uow := repos.AccountAssets.(*memoryAccountCharacterAssetUnitOfWork)
			err := uow.WithinAccountCharacterPetAssets(ctx, "dnf:1", "77", func(
				account repository.AccountInventoryRepository,
				character repository.CharacterRepository,
				inventory repository.InventoryRepository,
				equipment repository.EquipmentRepository,
				pet repository.PetRepository,
			) error {
				return test.run(ctx, account, character, inventory, equipment, pet)
			})
			if err != nil {
				t.Fatalf("foreign key check failed: %v", err)
			}
			assertAccountCharacterPetAssetSnapshot(t, before, loadAccountCharacterPetAssetSnapshot(t, ctx, repos))
		})
	}
}

type accountCharacterPetAssetSnapshot struct {
	account   repository.AccountInventoryRecord
	character repository.CharacterRecord
	inventory repository.InventoryRecord
	equipment repository.EquipmentRecord
	pet       repository.PetRecord
}

func loadAccountCharacterPetAssetSnapshot(t *testing.T, ctx context.Context, repos repository.Group) accountCharacterPetAssetSnapshot {
	t.Helper()
	account, _, err := repos.AccountInventory.Load(ctx, "dnf:1")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := repos.Character.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	inventory, _, err := repos.Inventory.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	equipment, _, err := repos.Equipment.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	pet, _, err := repos.Pet.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	return accountCharacterPetAssetSnapshot{
		account:   account,
		character: character,
		inventory: inventory,
		equipment: equipment,
		pet:       pet,
	}
}

func assertAccountCharacterPetAssetSnapshot(t *testing.T, want, got accountCharacterPetAssetSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(want.account, got.account) {
		t.Fatalf("account inventory changed after rollback: want=%+v got=%+v", want.account, got.account)
	}
	if !reflect.DeepEqual(want.character, got.character) {
		t.Fatalf("character changed after rollback: want=%+v got=%+v", want.character, got.character)
	}
	if !reflect.DeepEqual(want.inventory, got.inventory) {
		t.Fatalf("inventory changed after rollback: want=%+v got=%+v", want.inventory, got.inventory)
	}
	if !reflect.DeepEqual(want.equipment, got.equipment) {
		t.Fatalf("equipment changed after rollback: want=%+v got=%+v", want.equipment, got.equipment)
	}
	if !reflect.DeepEqual(want.pet, got.pet) {
		t.Fatalf("pet changed after rollback: want=%+v got=%+v", want.pet, got.pet)
	}
}

func seedAccountCharacterPetAssetTransaction(t *testing.T, ctx context.Context, repos repository.Group) {
	t.Helper()
	if repos.AccountAssets == nil {
		t.Fatal("account asset unit of work is missing")
	}
	if err := repos.AccountInventory.Save(ctx, repository.AccountInventoryRecord{
		AccountID: "dnf:1",
		Slots: map[string]repository.ItemStack{
			"0:354": {ItemID: 3033, Count: 5, Extra: map[string]string{"origin": "account"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Character.Save(ctx, repository.CharacterRecord{
		CharacterID: "77",
		AccountID:   "dnf:1",
		Stats:       map[string]int64{"gold": 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repository.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]repository.ItemStack{
			"0:9": {ItemID: 700, Count: 1, Extra: map[string]string{"origin": "character"}},
			"7:3": {ItemID: 63000, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, repository.EquipmentRecord{
		CharacterID: "77",
		Entries:     map[string]repository.EquipmentEntry{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Pet.Save(ctx, repository.PetRecord{
		CharacterID: "77",
		Entries: map[string]repository.PetEntry{
			"9001": {PetKey: "9001", ItemID: 63000, Level: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func mutateAccountCharacterPetAssets(
	ctx context.Context,
	account repository.AccountInventoryRepository,
	character repository.CharacterRepository,
	inventory repository.InventoryRepository,
	equipment repository.EquipmentRepository,
	pet repository.PetRepository,
) error {
	accountRecord, _, err := account.Load(ctx, "dnf:1")
	if err != nil {
		return err
	}
	characterRecord, _, err := character.Load(ctx, "77")
	if err != nil {
		return err
	}
	inventoryRecord, _, err := inventory.Load(ctx, "77")
	if err != nil {
		return err
	}
	equipmentRecord, _, err := equipment.Load(ctx, "77")
	if err != nil {
		return err
	}
	petRecord, _, err := pet.Load(ctx, "77")
	if err != nil {
		return err
	}

	stack := accountRecord.Slots["0:354"]
	stack.Count--
	accountRecord.Slots["0:354"] = stack
	characterRecord.Stats["gold"] += 25
	inventoryStack := inventoryRecord.Slots["0:9"]
	inventoryStack.Count++
	inventoryRecord.Slots["0:9"] = inventoryStack
	if equipmentRecord.Entries == nil {
		equipmentRecord.Entries = make(map[string]repository.EquipmentEntry)
	}
	equipmentRecord.Entries["24"] = repository.EquipmentEntry{SlotIndex: 24, ItemID: 63000}
	petRecord.EquippedKey = "9001"
	petEntry := petRecord.Entries["9001"]
	petEntry.Exp = 7
	petRecord.Entries["9001"] = petEntry

	if err := account.Save(ctx, accountRecord); err != nil {
		return err
	}
	if err := character.Save(ctx, characterRecord); err != nil {
		return err
	}
	if err := inventory.Save(ctx, inventoryRecord); err != nil {
		return err
	}
	if err := equipment.Save(ctx, equipmentRecord); err != nil {
		return err
	}
	return pet.Save(ctx, petRecord)
}

type accountPetFaultStore struct {
	repository.PetRepository
	mode    string
	saveErr error
	failed  bool
}

func (s *accountPetFaultStore) Save(ctx context.Context, record repository.PetRecord) error {
	if s.failed {
		return s.PetRepository.Save(ctx, record)
	}
	s.failed = true
	if err := s.PetRepository.Save(ctx, record); err != nil {
		return err
	}
	if s.mode == "pet_panic" {
		panic("pet save after physical write")
	}
	return s.saveErr
}

type accountPetCancelAfterSave struct {
	repository.PetRepository
	cancel context.CancelFunc
	used   bool
}

func (s *accountPetCancelAfterSave) Save(ctx context.Context, record repository.PetRecord) error {
	if err := s.PetRepository.Save(ctx, record); err != nil {
		return err
	}
	if !s.used {
		s.used = true
		s.cancel()
	}
	return nil
}
