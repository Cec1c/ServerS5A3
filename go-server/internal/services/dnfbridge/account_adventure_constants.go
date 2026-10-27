package dnfbridge

import (
	"longheng.io/server/internal/modules/dnf/adventuregroup"
)

// Verified in the distributed EXE native enum tables and registrations:
// noti1339/E63070, cmd1401/E650D0, noti1345/E5F8C0.
// Legacy CmdPacket enum ordinals differ after late additions; do not reuse them.
const (
	currentAdventureInfoRequestMsgID          uint16 = 1401
	currentAdventureExpeditionCancelMsgID     uint16 = 1405
	currentAdventureExpeditionRewardMsgID     uint16 = 1406
	currentAdventurePointRecalculateMsgID     uint16 = 1407
	currentAdventureShopPurchaseMsgID         uint16 = 1412
	currentAdventureGrowthCapsuleMsgID        uint16 = 1496
	currentAdventureRepresentCharactersMsgID  uint16 = 1473
	currentAdventureSelectRepresentativeMsgID uint16 = 1474
	currentAdventureExpeditionStateMsgID      uint16 = 1341

	currentAdventureInfoPushMsgID          = uint16(1339)
	currentAdventureActorRefreshMsgID      = uint16(1345)
	currentAdventureInfoRequestWireLength  = 16
	currentAdventureInfoSceneRequestLength = 2
	currentAdventureInfoRawLength          = 7424
	currentAdventureActorRefreshRawLength  = 16
	currentAdventureInfoManageLevelOffset  = 0
	currentAdventureInfoCurrentPointOffset = 8
	currentAdventureInfoLoginDaysOffset    = 20
	// 现用EXE E654E3..E6555B -> BEE0D8..BEE147；BE8BA0..BE8D09
	// 按界面顺序读取推荐、决斗、绝望、悲叹、安徒恩、卢克和超时空。
	currentAdventureInfoRecommendedOffset = 16
	currentAdventureInfoPVPWinOffset      = 32
	currentAdventureInfoDespairOffset     = 24
	currentAdventureInfoGriefOffset       = 28
	currentAdventureInfoAntonOffset       = 36
	currentAdventureInfoLukeOffset        = 40
	currentAdventureInfoFiendWarOffset    = 7420
	// E654E3/E6552B copy raw+40/+48 to manager+EC/+F4; BEE108/BEE120
	// project them to basic-info UI+494/+49C. BDF380 reads job+498,
	// base transfer+499 and the genuine honor level+49C for the selected
	// collection's highest-proficiency card. Other header bytes stay neutral.
	currentAdventureInfoBestCollectionJobOffset   = 44
	currentAdventureInfoBestCollectionGrowOffset  = 45
	currentAdventureInfoBestCollectionHonorOffset = 48
	currentAdventureInfoRosterOffset              = 60
	currentAdventureInfoRosterCount               = 100
	currentAdventureInfoRosterEntrySize           = 52
	// currentAdventureInfoRosterHonorLevelOffset is the u32 the current EXE's
	// character-collection/冒险团 card reads as that character's 名誉等级
	// (honor level). Live evidence: the value previously written here was the
	// numeric character id, and the client rendered exactly that id as the
	// honor level (character_id 2 -> "挑战者 02"). The character id itself is
	// carried by the separate op1467/op1468 representative row at +12.
	currentAdventureInfoRosterHonorLevelOffset = 4
	currentAdventureInfoRosterNameOffset       = 12
	currentAdventureInfoRosterNameSize         = 30
	// E6120C -> native row+2C -> BF258F card+34; BEB447
	// compares it with the lookup row's server group when restoring selection.
	currentAdventureInfoRosterServerGroupOffset = 42
	currentAdventureInfoRosterCardLevelOffset   = 48
	currentAdventureInfoCharacterCountOffset    = 56
	currentAdventureInfoShopPointOffset         = 5260
	currentAdventureInfoShopPointCount          = 5
	currentAdventureInfoShopPointEntrySize      = 8
	currentAdventureInfoPurchaseOffset          = 5300
	currentAdventureInfoPurchaseCount           = 256
	currentAdventureInfoPurchaseEntrySize       = 8
	// E65567/E655A4 source is EBP-0x2CA + 0x9A = raw+7348.
	// E63CF3 updates the same native +0x98 array after selecting a card.
	// These triples are collection counters, not dungeon-clear activity.
	currentAdventureInfoTripleOffset         = 7348
	currentAdventureInfoTripleCount          = 20
	currentAdventureInfoTripleSize           = 3
	currentAdventureInfoTripleCountOffset    = 1
	currentAdventureInfoTripleTypeOffset     = 0
	currentAdventureInfoGrowthCapsuleOffset  = 7408
	currentAdventureInfoTailUint32Offset     = 7416
	currentSelectorAdventureInfoSlotMetadata = adventuregroup.SelectorSlotMetadataKey
)
