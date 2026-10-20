package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	dnfcharacterdata "longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfprofession "longheng.io/server/internal/modules/dnf/profession"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type currentPVPPrivateIntent struct {
	job, grow byte
	expires   time.Time
}

func (s *Service) loadPVPPrivateCreation(job, grow byte) (*dnfpvp.PrivateCreation, error) {
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	return dnfpvp.LoadPrivateCreation(archive, job, grow)
}

func (s *Service) handleCheckPVPPrivateCharacter(session *gameSession, body []byte) error {
	const opcode = uint16(dnfenum.CmdPacketCheckPVPPrivateCharacter)
	response := []byte{0, 2}
	if session == nil {
		return nil
	}
	session.pvpPrivateCreate = nil
	if len(body) != 8 || binary.LittleEndian.Uint32(body) > 255 || binary.LittleEndian.Uint32(body[4:]) > 5 {
		s.logGameEvent(session, "game-pvp-private-check-invalid", "body_len", len(body))
		return s.sendGameUpperRawClass(session, opcode, response, dnfproto.DefaultChannelClassification)
	}
	job, grow := byte(binary.LittleEndian.Uint32(body)), byte(binary.LittleEndian.Uint32(body[4:]))
	plan, err := s.loadPVPPrivateCreation(job, grow)
	rating := int64(0)
	if err == nil {
		repos, ok := s.repositoryGroup()
		if !ok || repos.Character == nil {
			err = errCharacterRepositoryMissing
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
			defer cancel()
			rating, err = plan.Check(ctx, repos.Character, s.accountIDForSession(session), time.Now().UTC())
		}
	}
	if err == nil {
		response = []byte{1, boolByte(rating > 0)}
		session.pvpPrivateCreate = &currentPVPPrivateIntent{job: job, grow: grow, expires: time.Now().Add(time.Minute)}
	} else {
		var rejection dnfpvp.PrivateCreateError
		if errors.As(err, &rejection) {
			response[1] = byte(rejection)
		}
	}
	s.logGameEvent(session, "game-pvp-private-check-result", "job", job, "grow", grow, "success", response[0], "result", response[1], "inherited_rp", rating, "error", err)
	return s.sendGameUpperRawClass(session, opcode, response, dnfproto.DefaultChannelClassification)
}

func (s *Service) initializePVPPrivateCreation(ctx context.Context, creation *dnfcharacterdata.Creation) error {
	character := &creation.Character
	job, valid := characterJobByte(*character)
	if !valid {
		return fmt.Errorf("invalid PVP private job")
	}
	plan, err := s.loadPVPPrivateCreation(job, byte(character.Stats["grow_type"]))
	if err != nil {
		return err
	}
	creation.PVPPrivate = plan
	profiles, catalog, err := s.currentProfessionResources(ctx)
	if err != nil {
		return err
	}
	skill, err := profiles.ApplySkillTransition(catalog, job, character.Level, dnfprofession.Transition{Kind: dnfprofession.KindClassChange, FirstGrowType: plan.Grow, NewGrowType: plan.Grow}, *creation.Skill, creation.Skill.Points)
	if err != nil {
		return err
	}
	creation.Skill = &skill
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return err
	}
	paths, err := initialEquipmentPathMap(archive)
	if err != nil {
		return err
	}
	text, err := archive.ReadText(plan.CharacterPath)
	if err != nil {
		return err
	}
	jobTag := initialEquipmentJobTag(text)
	var equipped []initialEquipmentEntry
	bagSlot := 9
	seen := map[int16]bool{}
	for _, item := range plan.Equipment {
		slot, ok := initialEquipmentSlotMap[item.Slot]
		if !ok {
			return fmt.Errorf("unknown PVP private equipment slot %q", item.Slot)
		}
		durability, equipType, itemPath, layers, err := initialEquipmentMetadata(archive, paths, item.ItemID, jobTag)
		if err != nil {
			return err
		}
		entry := initialEquipmentEntry{Slot: slot, ItemID: item.ItemID, Durability: durability, EquipType: equipType, PVFPath: itemPath, ModelLayers: layers,
			RawEntry: buildInitialEquipmentRawEntry(slot, item.ItemID, durability)}
		if item.Equipped {
			if seen[slot] {
				return fmt.Errorf("duplicate PVP private equipped slot %d", slot)
			}
			seen[slot] = true
			equipped = append(equipped, entry)
		} else {
			if bagSlot >= 64 {
				return fmt.Errorf("PVP private equipment bag is full")
			}
			built := initialEquipmentRecord(character.CharacterID, []initialEquipmentEntry{entry}, character.UpdatedAt).Entries[strconv.Itoa(int(slot))]
			built.Extra["source"], built.Extra["item_kind"] = "pvf_pvp_private_create_equipment_list", "equipment"
			stack := dnfrepo.ItemStack{ItemID: item.ItemID, Count: 1, Extra: built.Extra}
			row := currentItemListEntryFromStack(0, int16(bagSlot), stack)
			stack.RawEntry = append([]byte(nil), row.data[:]...)
			creation.Inventory.Slots[fmt.Sprintf("0:%d", bagSlot)] = stack
			bagSlot++
		}
	}
	equipment := initialEquipmentRecord(character.CharacterID, equipped, character.UpdatedAt)
	creation.Equipment = &equipment
	if seen[21] {
		character.Stats["ex_equip_slot_stat"] |= int64(dnfquest.ExEquipSlotSupport)
	}
	if seen[22] {
		character.Stats["ex_equip_slot_stat"] |= int64(dnfquest.ExEquipSlotMagicStone)
	}
	return nil
}
