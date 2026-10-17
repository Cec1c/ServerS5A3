# QST-012 友军击杀后的死亡重放与跨房归属

## 状态

服务端候选已完成；完整领域、bridge、control、list、vet、Windows构建和真实PVF专项通过。隔离数据库未配置，条件跳过不计SQL验证。部署和实机验收另行记录；无需修改EXE或PVF。

## 范围

反馈涉及「天空之城的邪恶魔力」「光之城主」「GBL教的神殿」，真实主线分别为 3188、3190、3197。验证使用真实任务接取、真实迷宫运行时、真实 actor 宣告和实际 `handleDungeonMonsterDeath` / `handleDungeonMoveMap`。固定 PVF SHA256 为：

```text
fef4f46f6c3096d10cd88c84579dea8bd4fffa18e5a75b4684a2e920864f5525
```

对象：

- 3188：DGN 15 quest maze 4，起始 `57953`，clear-map `57962`。起始房真实 friend monster 65488 同时声明 `[monster team]=0` 与 `[monster follow type]=follow`；目标房没有本地友军。
- 3190：DGN 15 quest maze 5，起始 `57971`，clear-map `57979`。起始房有 friend AIC；目标房有 AIC 和 team=0 monster，其中 65488 带 follow，65512 不带 follow。
- 3197：DGN 21 `outerwall.dgn` quest maze 1，起始 `53351`，clear-map `53357`。起始房有 friend AIC；目标房没有本地友军。

## 成因

现有 sentinel 证明只识别 `owner=ffff + combat entries`。当前客户端也会直接写真实友军 objectKey，因此：

1. 首次 friendly owner 死亡走普通死亡事务，op38 可以正常返回。
2. 同一权威死亡被重复上报时，普通路径只看到 already-defeated，没有重放 op38。
3. 首次 op38 写失败时，死亡和资产已经提交；恢复连接后的同一请求仍未重放 op38。
4. MAP 模型已有 monster team，但没有同索引 follow 字段，跨房 monster owner 只能误落入只识别 AIC 的旧归属路径。

## 修复

- `worldmap.Map` 新增 `MonsterFollowType`，从 `[monster follow type]` 按 monster 索引解析，并在 map clone 中独立复制。
- 新增真实 friend owner 证明：
  - 当前房：已宣告的非敌对 AIC，或同索引 `[monster team]=0` 的已宣告 monster。
  - 已访问房：已宣告的非敌对 AIC；monster 额外要求原房间同索引 follow 为 `follow`。
  - 目标必须是当前房已宣告或已权威死亡的普通敌对 monster；显式同索引 team=0 的目标拒绝。缺少 team 行仍按既有规则视为敌对。
- 首次死亡继续走原 `CommitActorDeathReport` 及既有经验、掉落、任务事务。目标已 defeated 且 owner 证明成立时，直接调用现有权威 op38 重放，不再次结算。
- 当前房普通actor的既有owner规则保持；新的友军证明不会把敌对actor认作友军。跨房新增许可只接受已宣告且原MAP同索引team0/follow的怪物，planned、未知、跨房无follow和跨房目标均不获本项许可。
- sentinel `ffff + contribution` 路径保持兼容。
- 判断只使用 MAP/AIC/monster 宣告、同索引字段和当前运行对象引用，不使用固定怪物编号或名称。

## 自动验证

固定 PVF 可通过以下环境变量接入；变量值应指向 SHA256 匹配的文件：

```powershell
$env:DNFBRIDGE_REAL_PVF_SMOKE='<matching Script.pvf>'
$env:DNF_WORLDMAP_REAL_PVF_SMOKE=$env:DNFBRIDGE_REAL_PVF_SMOKE
```

真实任务专项：

```powershell
go test -buildvcs=false -count=1 -v -run '^TestSeekerQST012FriendlyAPCDeathsRealPVF$' ./internal/services/dnfbridge
```

结果：

- 3188 以起始房 friend monster 的原 objectKey 跨房完成 `57962`，最终 `active/0`、`completion_map_id=57962`。
- 3190 的 cross-room AIC 与 target-room follow monster 均完成普通怪死亡，房间清空并完成 `57979`。
- 3197 的 cross-room AIC 完成 `53357`。
- 三项均覆盖重复死亡和首次 op38 写失败后的重试；重放前后角色、背包、任务快照不变。

定向 owner/解析回归：

```powershell
go test -buildvcs=false -count=1 -v -run '^(TestSeekerQST012FriendlyAPCDeathsRealPVF|TestSeekerQST012FriendlyOwnerProof|TestDungeonRuntimeContainsDeathOwnerObjectKey|TestOfficialFriendlyAPC.*|TestOfficialFriendlyMonster.*)$' ./internal/services/dnfbridge
go test -buildvcs=false -count=1 -v -run '^(TestSeekerQST012MonsterFollowTypeParsesAndClones|TestParseMap)' ./internal/modules/dnf/worldmap
```

覆盖并通过：非法/敌对/未宣告 owner、跨房目标、跨房 team=0 但未 follow、follow 位于错误索引、unbound follower、follow 字段解析及 clone 隔离，以及既有 FriendlyAPC/FriendlyMonster/sentinel 回归。

中间房仍使用既有 smoke helper 定位路线；smoke 清房不计作友军战斗证据。自动测试不替代客户端画面、返回城镇、断线与重登验收。

## 实机验收

使用未使用完成券的合法角色：

- 3188：23 级，3187 已完成。
- 3190：23 级，3189 已完成。
- 3197：24 级，3196 已完成。

分别正常进图，让任务真实友军击杀普通怪，观察尸体消失、op38 死亡通知、门开启、下一房间和 clear-map 进度。3188 与 3197 的目标房没有本地友军，必须确认跨房后仍携带原 objectKey。最终检查任务可交、退出返回城镇不报错；重登后确认任务状态、完成键和 `completion_map_id` 保持。
