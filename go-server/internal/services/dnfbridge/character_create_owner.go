package dnfbridge

import (
	"context"
	"fmt"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	"time"

	dnfcharacterdata "longheng.io/server/internal/modules/dnf/characterdata"
	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) saveNewCharacter(ctx context.Context, repos dnfrepo.Group, record dnfrepo.CharacterRecord) error {
	now := record.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	init, initErr := s.newCharacterPVFInitialization(ctx, record)
	if initErr != nil {
		s.logPacketEvent("dnf-character-pvf-initialization-blocked",
			"character_id", record.CharacterID,
			"job", record.Job,
			"level", record.Level,
			"error", initErr)
		return fmt.Errorf("initialize new character from pvf: %w", initErr)
	}
	if init.HasStats {
		applyCharacterPVFStats(&record, init.Stats)
	}
	var welcomeCera int64
	if s.signupBenefits != nil {
		policy, err := s.signupBenefits.Policy(ctx)
		if err != nil {
			return fmt.Errorf("load character benefits: %w", err)
		}
		if policy.CharacterGold > 0 {
			if record.Stats == nil {
				record.Stats = make(map[string]int64)
			}
			record.Stats["gold"] = policy.CharacterGold
		}
		welcomeCera, err = s.signupBenefits.SignupCera(ctx, record.AccountID)
		if err != nil {
			return fmt.Errorf("load signup benefits: %w", err)
		}
	}
	var equipment *dnfrepo.EquipmentRecord
	if built := initialEquipmentRecord(record.CharacterID, init.Equipment, now); len(built.Entries) > 0 {
		equipment = &built
	}
	skill := initialSkillRecord(record, init, now)

	settings := s.newCharacterCSharpDefaultSettings(ctx, record, now)
	if init.Job == darkcombo.Job {
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return err
		}
		s.initialSkillsMu.Lock()
		catalog := s.skillCatalog
		s.initialSkillsMu.Unlock()
		pages, err := darkcombo.BuildDefaultPages(archive, catalog, skill)
		if err != nil {
			return err
		}
		if len(pages) == 0 {
			return fmt.Errorf("新黑武没有PVF连招根技能")
		}
		comboSettings, err := darkcombo.BuildSettingsRecord(record.CharacterID, pages, now)
		if err != nil {
			return err
		}
		settings = append(settings, comboSettings)
	}
	creation := dnfcharacterdata.Creation{
		WelcomeCera: welcomeCera,
		Account: &dnfrepo.AccountRecord{
			AccountID: record.AccountID,
			State:     "active",
			Metadata:  map[string]string{"source": "dnfbridge"},
			CreatedAt: record.CreatedAt,
			UpdatedAt: now,
		},
		Character: record,
		Inventory: ptrInventoryRecord(newCharacterInventoryRecord(record.CharacterID, now)),
		Equipment: equipment,
		Skill:     &skill,
		Settings:  settings,
	}
	if dnfpvp.IsPrivateCharacter(record) {
		if err := s.initializePVPPrivateCreation(ctx, &creation); err != nil {
			return err
		}
	}
	if err := dnfcharacterdata.NewCreator(repos).Create(ctx, creation); err != nil {
		return fmt.Errorf("persist new character initialization: %w", err)
	}
	if len(init.Equipment) > 0 {
		s.logPacketEvent("dnf-character-initial-equipment-seeded",
			"character_id", record.CharacterID,
			"job", record.Job,
			"count", len(init.Equipment),
			"source", "pvf_create_equipment_list")
	}
	return nil
}

func ptrInventoryRecord(record dnfrepo.InventoryRecord) *dnfrepo.InventoryRecord {
	return &record
}
