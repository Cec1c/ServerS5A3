---
category: 功能修复
---

# 多人队长可以邀请“单人残留队”的玩家

## 变更

- 队友走光后，剩下的人仍挂在只有自己的队伍里（管理器只在 0 人时删除队伍）。多人队长邀请这样的玩家时，邀请登记把它判成“目标已在别的队伍”，静默拒绝：邀请方只收到通用失败，被邀请方什么都收不到，现场表现为“还没进图，第三个人怎么都组不进来”。
- 现在目标若只是自己一人的残留队队长，按无队处理并登记邀请；接受时原有 `Join` 先把他从残留队摘下再加入，残留队随之删除。
- 目标在真正的多人队里、或不是残留队队长时仍然拒绝；目标还在组队副本实例里时也维持原拒绝，并记录 `game-peer-request-invite-deferred reason=target_still_in_party_dungeon_instance`。

## 验证

- 新增 `party` 包两个用例：残留队成员被多人队邀请、接受后进入三人队且残留队删除；真实多人队成员与非队长邀请仍被拒绝。修复前前者失败。
- `go build`、`go vet -tags=timetzdata`、`gofmt` 通过；dnfbridge 邀请/接受/重组相关 8 个用例通过。`TestChannelReconnectTownArrivalsCanInviteEachOther` 与 `TestInviteToCharacterWithoutPublishedTownPresenceIsStillRefused` 在改动前即因夹具缺少在线活动角色失败，与本改动无关。
- 游戏内多人邀请待实机验收。
