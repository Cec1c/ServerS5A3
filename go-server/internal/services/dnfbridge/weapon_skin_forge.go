package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math/bits"
	"strings"
	"time"

	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemlock"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/weaponskin"
)

const (
	weaponSkinForgeNPC  = 1 // 90 与 95 的 npc/npc.lst 均以 1 对应 Linus.npc。
	forgeSelect         = 0
	forgeConfirm        = 1
	forgeRegister       = 2
	forgeInventory      = 3
	forgePickSlot       = 4
	forgeNoSlot         = -1
	forgeLastWeaponSlot = 64 // 90 主背包装备页末格。
)

type weaponSkinForgeRequest struct {
	id, epoch uint32
	action    byte
	recipe    weaponskin.BeiyunRecipe
	slot      int16
	token     uint64
	catalog   bool
}

// 单次确认只适用于本连接、选角代次与读取时的具体武器。
type weaponSkinForgeSelection struct {
	bound                boundGameSessionCharacter
	epoch                uint32
	token                uint64
	availableSlots       uint64
	expires              time.Time
	recipe               weaponskin.BeiyunRecipe
	weaponSlot, moldSlot int16
	weapon               dnfrepo.ItemStack
	catalog              bool
}

func decodeWeaponSkinForge(body []byte) (weaponSkinForgeRequest, bool) {
	if len(body) != 24 || (string(body[:4]) != "WSF1" && string(body[:4]) != "WSF2") || body[13] > byte(weaponskin.SecretRecipe) {
		return weaponSkinForgeRequest{}, false
	}
	r := weaponSkinForgeRequest{
		id: binary.LittleEndian.Uint32(body[4:8]), epoch: binary.LittleEndian.Uint32(body[8:12]),
		action: body[12], recipe: weaponskin.BeiyunRecipe(body[13]),
		slot: int16(binary.LittleEndian.Uint16(body[14:16])), token: binary.LittleEndian.Uint64(body[16:24]),
		catalog: body[3] == '2',
	}
	hasToken := r.action == forgeConfirm || r.catalog && r.action == forgeRegister
	if r.id == 0 || r.epoch == 0 || hasToken != (r.token != 0) {
		return r, false
	}
	switch r.action {
	case forgeInventory:
		return r, r.slot == forgeNoSlot
	case forgeSelect:
		return r, r.slot >= forgeNoSlot
	case forgePickSlot:
		return r, r.slot >= 1 && r.slot <= forgeLastWeaponSlot
	case forgeConfirm, forgeRegister:
		return r, r.slot >= 0
	default:
		return r, false
	}
}

func resolveBeiyunForgePlacement(catalog *pvfDungeonDropCatalog, recipe weaponskin.BeiyunRecipe) (dnfinventory.BeiyunForgePlacement, bool) {
	a, _ := weaponskin.FromAppearance(weaponskin.SampleID)
	return resolveWeaponSkinForgePlacement(catalog, recipe, a)
}

func resolveWeaponSkinForgePlacement(catalog *pvfDungeonDropCatalog, recipe weaponskin.BeiyunRecipe, a weaponskin.Appearance) (dnfinventory.BeiyunForgePlacement, bool) {
	items := a.Items(recipe)
	weapon, weaponErr := catalog.ResolveItem(a.SourceID)
	mold, moldErr := catalog.ResolveItem(items.MoldID)
	product, productErr := catalog.ResolveItem(items.ProductID)
	// 装备索引保留大小写，幻化目录按 PVF 的大小写不敏感查找规则生成。
	if weaponErr != nil || moldErr != nil || productErr != nil || !weaponSkinAssetReady(catalog, a) ||
		weapon.Kind != dungeonDropItemEquipment || !strings.EqualFold(weapon.PVFPath, a.SourcePath) || weapon.EquipmentType != "[weapon]" ||
		mold.Kind != dungeonDropItemStackable || mold.PVFPath != items.MoldPath || mold.StackableType != "[material]" ||
		product.Kind != dungeonDropItemStackable || product.PVFPath != items.ProductPath ||
		product.StackableType != "[waste]" || product.ActionType != "" ||
		(recipe == weaponskin.SecretRecipe && (mold.AttachType != "[account]" || product.AttachType != "[account]")) {
		return dnfinventory.BeiyunForgePlacement{}, false
	}
	return dnfinventory.BeiyunForgePlacement{
		Recipe: recipe, Appearance: a,
		Weapon: currentPVFItemPlacementPolicy(weapon), Mold: currentPVFItemPlacementPolicy(mold), Product: currentPVFItemPlacementPolicy(product),
	}, true
}

func (s *Service) handleWeaponSkinForge(session *gameSession, body []byte) error {
	r, ok := decodeWeaponSkinForge(body)
	if !ok || s.options.currentClientVersion != "official-24.9" {
		return nil
	}
	identity, bound, ok := s.playerStatsGameIdentity(session)
	if !ok {
		return nil
	}
	now := s.gameplayNow()
	current := func() bool {
		return s.boundGameSessionCharacterCurrent(bound) && !session.loginRevoked.Load() &&
			session.townSceneReadyCharacterID == bound.character && !currentDungeonSceneActive(session)
	}
	if !current() || (r.action != forgeRegister && s.validateUpgradeNPCScene(bound, weaponSkinForgeNPC) != nil) {
		return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
	}
	if now.Sub(session.weaponSkinForgeLastRequest) < 150*time.Millisecond {
		return s.sendWeaponSkinForge(session, r, 8, weaponSkinForgeSelection{})
	}
	session.weaponSkinForgeLastRequest = now
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
	}
	placement, ok := resolveBeiyunForgePlacement(catalog, r.recipe)
	if !ok {
		return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
	}
	ctx, cancel := context.WithTimeout(session.ctx, createWriteTimeout)
	defer cancel()
	repos, ok := s.repositoryGroupFor(ctx)
	if !ok || repos.Character == nil || repos.CharacterAssets == nil || repos.Inventory == nil {
		return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
	}
	actor := dnfinventory.WeaponSkinActor{CharacterID: identity.CharacterID, AccountID: identity.AccountID, Now: now, Current: current}
	resolve := func(id int64) (dnfinventory.BeiyunForgePlacement, bool) {
		if id == int64(weaponskin.BeiyunWeaponID) {
			return placement, true
		}
		if !r.catalog || id <= 0 || id > 0xffffffff {
			return dnfinventory.BeiyunForgePlacement{}, false
		}
		a, ok := weaponskin.FromSource(uint32(id))
		if !ok {
			return dnfinventory.BeiyunForgePlacement{}, false
		}
		return resolveWeaponSkinForgePlacement(catalog, r.recipe, a)
	}
	owner, err := dnfinventory.NewOwner(repos)
	if err != nil {
		return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
	}
	var changed []int16
	reason := "林纳斯武器幻化"
	switch r.action {
	case forgeInventory:
		bag, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, placement, r.catalog, forgeNoSlot, resolve)
		if err != nil {
			return s.sendWeaponSkinForge(session, r, weaponSkinForgeStatus(err), weaponSkinForgeSelection{})
		}
		if !current() {
			return nil
		}
		return s.sendWeaponSkinForge(session, r, 0, weaponSkinForgeSelection{
			weaponSlot: forgeNoSlot, moldSlot: bag.moldSlot, availableSlots: bag.allowed,
		})
	case forgeSelect, forgePickSlot:
		session.weaponSkinForgeSelection = weaponSkinForgeSelection{}
		weaponSlot := int16(forgeNoSlot)
		if r.action == forgePickSlot {
			weaponSlot = r.slot
		}
		bag, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, placement, r.catalog, weaponSlot, resolve)
		if err != nil {
			return s.sendWeaponSkinForge(session, r, weaponSkinForgeStatus(err), weaponSkinForgeSelection{})
		}
		selection, err := pickWeaponSkinForgeSlot(bag, r.recipe, r.slot, r.action == forgePickSlot)
		if err != nil {
			return s.sendWeaponSkinForge(session, r, weaponSkinForgeStatus(err), weaponSkinForgeSelection{})
		}
		if !current() {
			return nil
		}
		selection.bound, selection.epoch, selection.expires = bound, r.epoch, now.Add(2*time.Minute)
		selection.catalog = r.catalog
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
		}
		selection.token = binary.LittleEndian.Uint64(nonce[:])
		if selection.token == 0 {
			return s.sendWeaponSkinForge(session, r, 1, weaponSkinForgeSelection{})
		}
		session.weaponSkinForgeSelection = selection
		return s.sendWeaponSkinForge(session, r, 0, selection)
	case forgeConfirm:
		selection := session.weaponSkinForgeSelection
		if selection.bound != bound || selection.epoch != r.epoch || selection.token != r.token ||
			selection.weaponSlot != r.slot || selection.recipe != r.recipe || selection.catalog != r.catalog || !selection.expires.After(now) {
			return s.sendWeaponSkinForge(session, r, 5, weaponSkinForgeSelection{})
		}
		session.weaponSkinForgeSelection = weaponSkinForgeSelection{}
		selectedPlacement, ok := resolve(selection.weapon.ItemID)
		if !ok {
			return s.sendWeaponSkinForge(session, r, 2, weaponSkinForgeSelection{})
		}
		result, err := owner.ForgeBeiyun(ctx, dnfinventory.BeiyunForgeRequest{
			WeaponSkinActor: actor, WeaponSlot: selection.weaponSlot, MoldSlot: selection.moldSlot, ExpectedWeapon: selection.weapon,
			AllowCrossJob: selection.catalog,
		}, selectedPlacement)
		if err != nil {
			return s.sendWeaponSkinForge(session, r, weaponSkinForgeStatus(err), weaponSkinForgeSelection{})
		}
		changed = result.ChangedSlots
	case forgeRegister:
		if r.catalog {
			a, ok := weaponskin.FromProduct(r.token, r.recipe)
			if !ok {
				return s.sendWeaponSkinForge(session, r, 6, weaponSkinForgeSelection{})
			}
			placement, ok = resolve(int64(a.SourceID))
			if !ok {
				return s.sendWeaponSkinForge(session, r, 6, weaponSkinForgeSelection{})
			}
		}
		if err := owner.UseBeiyunAppearance(ctx, actor, r.slot, placement); err != nil {
			return s.sendWeaponSkinForge(session, r, weaponSkinForgeStatus(err), weaponSkinForgeSelection{})
		}
		changed = []int16{r.slot}
		reason = "武器幻化成品入库"
	}
	for _, slot := range changed {
		if !current() {
			return nil
		}
		if err := s.sendCurrentSocketInventorySlotUpdate(session, dnfrepo.MainInventoryListType, slot, reason); err != nil {
			return err
		}
	}
	if !current() {
		return nil
	}
	return s.sendWeaponSkinForge(session, r, 0, weaponSkinForgeSelection{})
}

type beiyunForgeBackpack struct {
	slots      map[string]dnfrepo.ItemStack
	allowed    uint64 // 位 slot-1 对应 90 主背包的实际装备槽位。
	registered uint64
	moldSlot   int16
}

func inspectWeaponSkinForgeBackpack(ctx context.Context, repos dnfrepo.Group, actor dnfinventory.WeaponSkinActor, placement dnfinventory.BeiyunForgePlacement, wider bool, weaponSlot int16, resolve func(int64) (dnfinventory.BeiyunForgePlacement, bool)) (beiyunForgeBackpack, error) {
	character, found, err := repos.Character.Load(ctx, actor.CharacterID)
	if err != nil {
		return beiyunForgeBackpack{}, err
	}
	if !found || character.CharacterID != actor.CharacterID || character.AccountID != actor.AccountID || (!wider && character.Job != "0") {
		return beiyunForgeBackpack{}, weaponskin.ErrCharacter
	}
	if !wider && weaponskin.BeiyunRegistered(character) {
		return beiyunForgeBackpack{}, weaponskin.ErrAlreadyRegistered
	}
	bag, found, err := repos.Inventory.Load(ctx, actor.CharacterID)
	if err != nil {
		return beiyunForgeBackpack{}, err
	}
	if !found || bag.CharacterID != actor.CharacterID {
		return beiyunForgeBackpack{}, dnfinventory.ErrInventoryNotFound
	}
	if err := dnfrepo.BindInventoryContainerState(ctx, repos.Settings, &bag); err != nil {
		return beiyunForgeBackpack{}, err
	}
	backpack := beiyunForgeBackpack{slots: bag.Slots, moldSlot: forgeNoSlot}
	moldID := int64(placement.Recipe.Items().MoldID)
	for key, item := range bag.Slots {
		list, slot, valid := dnfrepo.ParseCurrentInventoryLocation(key)
		if !valid {
			continue
		}
		if list == dnfrepo.MainInventoryListType && slot >= 1 && slot <= forgeLastWeaponSlot &&
			(weaponSlot == forgeNoSlot || slot == weaponSlot) {
			if selected, ok := resolve(item.ItemID); ok && (wider || selected.Target().Allows(character.Job)) && selected.AllowsWeapon(&bag, list, slot, item, actor.Now) {
				mask := uint64(1) << (slot - 1)
				if weaponskin.Registered(character, selected.Target()) {
					backpack.registered |= mask
				} else {
					backpack.allowed |= mask
				}
			}
		}
		if item.ItemID == moldID && item.Count > 0 && (item.ExpireAt.IsZero() || item.ExpireAt.After(actor.Now)) &&
			!itemlock.IsLockedAt(item, actor.Now) && placement.Mold.ForInventory(&bag).Contains(list, slot) &&
			(backpack.moldSlot < 0 || slot < backpack.moldSlot) {
			backpack.moldSlot = slot
		}
	}
	return backpack, nil
}

func prepareBeiyunForgeSelection(ctx context.Context, repos dnfrepo.Group, actor dnfinventory.WeaponSkinActor, placement dnfinventory.BeiyunForgePlacement, requested int16, exact bool) (weaponSkinForgeSelection, error) {
	weaponSlot := int16(forgeNoSlot)
	if exact {
		weaponSlot = requested
	}
	backpack, err := inspectWeaponSkinForgeBackpack(ctx, repos, actor, placement, false, weaponSlot, func(id int64) (dnfinventory.BeiyunForgePlacement, bool) {
		return placement, id == int64(placement.Target().SourceID)
	})
	if err != nil {
		return weaponSkinForgeSelection{}, err
	}
	return pickWeaponSkinForgeSlot(backpack, placement.Recipe, requested, exact)
}

func pickWeaponSkinForgeSlot(backpack beiyunForgeBackpack, recipe weaponskin.BeiyunRecipe, requested int16, exact bool) (weaponSkinForgeSelection, error) {
	if exact {
		if requested >= 1 && requested <= forgeLastWeaponSlot && backpack.registered&(uint64(1)<<(requested-1)) != 0 {
			return weaponSkinForgeSelection{}, weaponskin.ErrAlreadyRegistered
		}
		if requested < 1 || requested > forgeLastWeaponSlot || backpack.allowed&(uint64(1)<<(requested-1)) == 0 {
			return weaponSkinForgeSelection{}, dnfinventory.ErrWeaponSkinWeapon
		}
	} else {
		if backpack.allowed == 0 {
			return weaponSkinForgeSelection{}, dnfinventory.ErrWeaponSkinWeapon
		}
		// 保留原有试验客户端的轮换入口。
		next := backpack.allowed
		if requested > 0 && requested < forgeLastWeaponSlot {
			next &= ^uint64(0) << requested
		} else if requested >= forgeLastWeaponSlot {
			next = 0
		}
		if next == 0 {
			next = backpack.allowed
		}
		requested = int16(bits.TrailingZeros64(next) + 1)
	}
	if backpack.moldSlot < 0 {
		return weaponSkinForgeSelection{}, dnfinventory.ErrWeaponSkinMold
	}
	return weaponSkinForgeSelection{
		weaponSlot: requested, moldSlot: backpack.moldSlot, recipe: recipe,
		weapon: backpack.slots[currentSocketInventoryKey(dnfrepo.MainInventoryListType, requested)],
	}, nil
}

func weaponSkinForgeStatus(err error) byte {
	switch {
	case errors.Is(err, dnfinventory.ErrWeaponSkinWeapon):
		return 2
	case errors.Is(err, dnfinventory.ErrWeaponSkinMold):
		return 3
	case errors.Is(err, dnfinventory.ErrWeaponSkinSpace):
		return 4
	case errors.Is(err, dnfinventory.ErrWeaponSkinItem):
		return 6
	case errors.Is(err, weaponskin.ErrAlreadyRegistered):
		return 7
	default:
		return 1
	}
}

func (s *Service) sendWeaponSkinForge(session *gameSession, r weaponSkinForgeRequest, status byte, selection weaponSkinForgeSelection) error {
	var body [28]byte
	copy(body[:4], "WFR1")
	binary.LittleEndian.PutUint32(body[4:8], r.id)
	binary.LittleEndian.PutUint32(body[8:12], r.epoch)
	body[12], body[13] = status, r.action
	binary.LittleEndian.PutUint16(body[14:16], uint16(selection.weaponSlot))
	binary.LittleEndian.PutUint64(body[16:24], selection.token)
	if r.action == forgeInventory {
		binary.LittleEndian.PutUint64(body[16:24], selection.availableSlots)
	}
	binary.LittleEndian.PutUint16(body[24:26], uint16(selection.moldSlot))
	return s.sendGameUpperFixed16Transport(session, weaponSkinDemoOpcode, body[:], 1, 0, false, "武器幻化制作回执")
}
