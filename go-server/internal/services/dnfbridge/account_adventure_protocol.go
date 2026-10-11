package dnfbridge

import (
	"encoding/binary"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/adventuregroup"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// buildCurrentAdventureActorRefreshBody follows current NoPack sub_E5F8C0.
func buildCurrentAdventureActorRefreshBody(
	currentObjectKey uint16,
	summary adventuregroup.Summary,
) []byte {
	raw := make([]byte, currentAdventureActorRefreshRawLength)
	binary.LittleEndian.PutUint32(raw[0:4], uint32(summary.ManageLevel))
	binary.LittleEndian.PutUint64(raw[8:16], summary.TotalPoint)

	var writer packetWriter
	writer.writeUint16(currentObjectKey)
	writer.writeUint32(uint32(len(raw)))
	writer.writeBytes(raw)
	writer.writeZero(4)
	return writer.bytes()
}

func buildCurrentAdventureInfoBody(
	characterID uint16,
	summary adventuregroup.Summary,
	characters []dnfrepo.CharacterRecord,
) []byte {
	return buildCurrentAdventureInfoBodyWithIdentity(characterID, summary, characters, "", 0)
}

func buildCurrentAdventureInfoBodyWithName(
	characterID uint16,
	summary adventuregroup.Summary,
	characters []dnfrepo.CharacterRecord,
	representAccountName string,
) []byte {
	return buildCurrentAdventureInfoBodyWithIdentity(
		characterID,
		summary,
		characters,
		representAccountName,
		0,
	)
}

// buildCurrentAdventureInfoBody follows sub_E650D0/sub_E63070 in the authoritative EXE.
func buildCurrentAdventureInfoBodyWithIdentity(
	characterID uint16,
	summary adventuregroup.Summary,
	characters []dnfrepo.CharacterRecord,
	representAccountName string,
	createdDate uint32,
) []byte {
	return buildCurrentAdventureInfoBodyWithState(
		characterID,
		summary,
		characters,
		representAccountName,
		createdDate,
		adventuregroup.Projection{},
	)
}

func buildCurrentAdventureInfoBodyWithState(
	characterID uint16,
	summary adventuregroup.Summary,
	characters []dnfrepo.CharacterRecord,
	representAccountName string,
	createdDate uint32,
	projection adventuregroup.Projection,
) []byte {
	raw := make([]byte, currentAdventureInfoRawLength)
	binary.LittleEndian.PutUint32(
		raw[currentAdventureInfoManageLevelOffset:currentAdventureInfoManageLevelOffset+4],
		uint32(summary.ManageLevel),
	)
	binary.LittleEndian.PutUint64(
		raw[currentAdventureInfoCurrentPointOffset:currentAdventureInfoCurrentPointOffset+8],
		summary.TotalPoint,
	)
	binary.LittleEndian.PutUint32(
		raw[currentAdventureInfoLoginDaysOffset:currentAdventureInfoLoginDaysOffset+4],
		projection.ConsecutiveLoginDays,
	)
	characterCount := len(characters)
	if characterCount > currentAdventureInfoRosterCount {
		characterCount = currentAdventureInfoRosterCount
	}
	binary.LittleEndian.PutUint16(
		raw[currentAdventureInfoCharacterCountOffset:currentAdventureInfoCharacterCountOffset+2],
		uint16(characterCount),
	)
	sourceGroup := projection.CollectionSourceServerGroup
	if sourceGroup == 0 {
		sourceGroup = 1 // Same configured-source default as options.channelServerID.
	}
	collectionRows := adventuregroup.CollectionRows(characters, projection.CollectionSelections, currentAdventureCharacterLevelCap)
	writeCurrentAdventureInfoRoster(raw, collectionRows, sourceGroup)
	writeCurrentAdventureInfoBestCollection(raw, collectionRows)
	for index, points := range projection.Runtime.ShopPoints {
		if index >= currentAdventureInfoShopPointCount {
			break
		}
		offset := currentAdventureInfoShopPointOffset + index*currentAdventureInfoShopPointEntrySize
		binary.LittleEndian.PutUint32(raw[offset:offset+4], points)
		binary.LittleEndian.PutUint32(raw[offset+4:offset+8], points)
	}
	purchaseIndex := 0
	for _, category := range []byte{
		adventuregroup.ShopPointBrave,
		adventuregroup.ShopPointGlory,
		adventuregroup.ShopPointPure,
	} {
		for _, product := range adventureGroupProductsForProjection(projection, category) {
			if purchaseIndex >= currentAdventureInfoPurchaseCount {
				break
			}
			offset := currentAdventureInfoPurchaseOffset + purchaseIndex*currentAdventureInfoPurchaseEntrySize
			binary.LittleEndian.PutUint32(raw[offset:offset+4], product.ItemID)
			binary.LittleEndian.PutUint32(raw[offset+4:offset+8], product.Count)
			purchaseIndex++
		}
	}
	for index := 0; index < currentAdventureInfoTripleCount; index++ {
		offset := currentAdventureInfoTripleOffset + index*currentAdventureInfoTripleSize
		binary.LittleEndian.PutUint16(
			raw[offset+currentAdventureInfoTripleCountOffset:offset+currentAdventureInfoTripleCountOffset+2],
			projection.CollectionCounts[index],
		)
		raw[offset+currentAdventureInfoTripleTypeOffset] = byte(index)
	}
	binary.LittleEndian.PutUint64(
		raw[currentAdventureInfoGrowthCapsuleOffset:currentAdventureInfoGrowthCapsuleOffset+8],
		projection.Runtime.GrowthExperience,
	)

	var writer packetWriter
	writer.writeUint16(characterID)
	writer.writeUint32(createdDate)
	writer.writeZero(4)
	writer.writeUint32(0)
	writer.writeUint32(uint32(len(raw)))
	writer.writeBytes(raw)
	name, err := encodeRepresentAccountName(representAccountName)
	if err != nil {
		name = nil
	}
	writer.writeRawDstr(name)
	return writer.bytes()
}

// The basic-info card comes from the character-collection choices, not from
// every owned character or the separately selected representative portrait.
// CollectionRows has already removed stale/ineligible choices and orders ties
// by slot/character id. A selected character with honor level zero is valid;
// its nonzero base transfer distinguishes it from an unselected collection.
func writeCurrentAdventureInfoBestCollection(raw []byte, collectionRows []dnfrepo.CharacterRecord) {
	var best dnfrepo.CharacterRecord
	var bestHonor uint32
	found := false
	for _, row := range collectionRows {
		if row.Name == "" {
			continue
		}
		honor := currentAdventureInfoRosterHonorLevel(row)
		if found && honor <= bestHonor {
			continue
		}
		best, bestHonor, found = row, honor, true
	}
	if !found {
		return
	}
	job, _ := strconv.Atoi(best.Job)
	raw[currentAdventureInfoBestCollectionJobOffset] = byte(job)
	raw[currentAdventureInfoBestCollectionGrowOffset] = characterGrowTypeByte(best) & currentAdventureRepresentGrowMask
	binary.LittleEndian.PutUint32(raw[currentAdventureInfoBestCollectionHonorOffset:currentAdventureInfoBestCollectionHonorOffset+4], bestHonor)
}

type adventureGroupProjectedPurchase struct {
	ItemID uint32
	Count  uint32
}

func adventureGroupProductsForProjection(
	projection adventuregroup.Projection,
	category byte,
) []adventureGroupProjectedPurchase {
	prefix := strconv.Itoa(int(category)) + ":"
	products := make([]adventureGroupProjectedPurchase, 0)
	for key, count := range projection.Runtime.Purchases {
		if count == 0 || !strings.HasPrefix(key, prefix) {
			continue
		}
		itemID, err := strconv.ParseUint(strings.TrimPrefix(key, prefix), 10, 32)
		if err != nil || itemID == 0 {
			continue
		}
		products = append(products, adventureGroupProjectedPurchase{ItemID: uint32(itemID), Count: count})
	}
	sort.Slice(products, func(left, right int) bool { return products[left].ItemID < products[right].ItemID })
	return products
}

func writeCurrentAdventureInfoRoster(raw []byte, characters []dnfrepo.CharacterRecord, sourceGroup byte) {
	count := len(characters)
	if count > currentAdventureInfoRosterCount {
		count = currentAdventureInfoRosterCount
	}
	for index := 0; index < count; index++ {
		entryOffset := currentAdventureInfoRosterOffset + index*currentAdventureInfoRosterEntrySize
		entry := raw[entryOffset : entryOffset+currentAdventureInfoRosterEntrySize]
		writeCurrentAdventureInfoRosterEntry(entry, characters[index], sourceGroup)
	}
}

func writeCurrentAdventureInfoRosterEntry(entry []byte, character dnfrepo.CharacterRecord, sourceGroup byte) {
	if len(entry) < currentAdventureInfoRosterEntrySize {
		return
	}
	entry = entry[:currentAdventureInfoRosterEntrySize]
	clear(entry)
	entry[0] = byte(numericCharacterStat(character.Job))
	// E610C0 retains this byte as the map key; BF23B0 looks up the PVF's
	// job/base-branch pair. Packed 0x21 cannot match the SwordMaster key 1.
	entry[1] = characterGrowTypeByte(character) & currentAdventureRepresentGrowMask
	// raw+4 is the character card's 名誉等级 (honor level). It must carry the
	// post-cap honour level, never the character id: the current EXE renders
	// this dword as the honour level (grade name from etc/honorlevel.etc
	// [expert info] + the level), which is why a Lv40 character whose id was 2
	// was displayed as "挑战者 02". Honour only accrues at the level cap, so an
	// under-cap character is 0 (expert grade 0 = 挑战者 with no medal/icon).
	binary.LittleEndian.PutUint32(
		entry[currentAdventureInfoRosterHonorLevelOffset:currentAdventureInfoRosterHonorLevelOffset+4],
		currentAdventureInfoRosterHonorLevel(character),
	)
	name := currentAdventureInfoRosterNameBytes(character.Name)
	copy(
		entry[currentAdventureInfoRosterNameOffset:currentAdventureInfoRosterNameOffset+currentAdventureInfoRosterNameSize],
		name,
	)
	level := rosterLevel(character)
	if group, valid := adventuregroup.CollectionServerGroup(character, sourceGroup); valid {
		entry[currentAdventureInfoRosterServerGroupOffset] = group
	}
	// Current NoPack sub_A100B0/sub_A105F0 projects raw+48 into the
	// collection-card field read by sub_9FDCF0 as the displayed Lv value.
	binary.LittleEndian.PutUint32(
		entry[currentAdventureInfoRosterCardLevelOffset:currentAdventureInfoRosterCardLevelOffset+4],
		uint32(level),
	)
}

func currentAdventureInfoRosterNameBytes(name string) []byte {
	encoded := make([]byte, 0, currentAdventureInfoRosterNameSize-1)
	for _, value := range name {
		part, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(string(value)))
		if err != nil || len(encoded)+len(part) >= currentAdventureInfoRosterNameSize {
			break
		}
		encoded = append(encoded, part...)
	}
	return encoded
}
