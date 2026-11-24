---
category: 功能修复
---

# 队长退队后保留队员之间的 UDP 中继端口，修复留下的队员网络异常

## 变更

- 队长退队时，服务端会让剩下的队员换一个新队伍号重建队伍。原来的做法是把旧队伍号的中继线路全部关掉，新队伍再从起始端口重新分配，队员之间每一对的端口都会被打乱。
- 客户端对已经认识的队友不会改用服务端新下发的端口，重推名册也没用，会继续往旧端口发包。而这些旧端口已经分给了别的队员组合，结果就是串线或单向不通。
- 线上 11-18 23:47～23:51 四人反复进退队实测：每次队长退队后都出现某个队员整段时间不往新端口发包（`party UDP relay silent peer kick` 连催 3 次无效）。客户端网络台账确认，换队长后 30 秒内它只往旧端口发包。普通队员退队、再邀请回来不会出问题，因为那只会增删那一个人的线路。
- 现在队长退队时，留下的队员之间每一对的端口都原样带到新队伍号。旧线路照常关闭，新队伍建线路时拿回同一个端口，客户端继续往原端口发包就能直接接上。日志记 `party UDP relay legs closed reason=leader_transfer` 和 `party UDP relay leader transfer kept directed ports`。
- 退队的原队长重新被邀请回来时，按新成员正常分配端口。队伍解散、普通退队、副本内队长退出的行为都不变。独立中继（remote 模式）仍按原样关闭，线上用的是内嵌中继。

## 验证

- 新增 3 个用例，复现现场的角色号和入队顺序：
  - 队长转移后留下队员的每一对端口都不变，原队长回队后也不会和其他人撞端口；
  - 对照用例证实旧的关闭再重建会打乱端口；
  - 用真实队伍管理器让队长退队，服务层把端口带到新队伍号；新队伍号无效时仍按原逻辑关闭，旧队伍号守卫不会误关仍在用的队伍。
- `go vet` 通过；`TestPartyReapplyPreservesDirectedPortsAcrossMemberTimeout`、`TestLeaderHandoffAfterCompletedRunKeepsUDPBothWaysBothRounds`、`TestFourPeerHandoffCompletedReentryAndDirectedUDP`、`TestChangePartyMemberPositionExplicitEmptySlot` 以及 party 模块测试通过。`TestOfficial249RoomTableKeepsFixedSlotHoleAfterLeave` 在改动前即失败，与本改动无关。
- 游戏内"四人组队→队长退队→再邀请"待实机验收。
