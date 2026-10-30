# R134 P133 德利拉收费、钱包与 MR140 保留核验

> 本文环境地址、账号和工具链路径已脱敏；示例地址不表示当前部署目标。

日期：2026-10-21（香港）。副本：`D:/codex-build-r134/skill-source`，HEAD `7ca5fe751560118883416833edecad09182da40e`。

## 结论

**已有修复完整保留；当前合法契约下未复现“不扣500”。本轮只新增真实 PVF 分派回归及报告，不重复修改业务或新建 MR。**

P133 原文位于 `E:/90/repair-round134-20261021/doc-audit/paragraphs.json:4870`，index=133：“通关副本后 修理商德利拉 分解装备本应扣除500金币未扣除”。当前测试可验证单件2000→1500、两件2000→1000、恰好500→0，仓库状态和下行金额一致。现场具体请求、运行二进制身份及原生显示验收仍未取得，本报告不把测试通过等同于用户已验收。

## MR 身份、历史部署与当前差异

通过 `D:/codex-build-r132/gitgud_proxy.py` 的 ProxyClient 只读 GET `/projects/51684/merge_requests/140` 和 `/changes`，当次返回：

- !140 `Draft: fix(disjoint): 德利拉原子扣费与金币刷新`，state=opened，draft=true，merged_at=null。
- 分支 `fix/delilah-disjoint-fee-20261019` → master。
- head `8f1b4be985642b4a98c06789bc94c08fa9c5ced9`；base `3a1c4bd64a416043bc57a7b79224115158ad98e0`。
- changes=9，overflow=false（七份Go文件、原报告、changlog）。未写入远端，没有新建或修改 MR。

该提交对象在 `E:/90/repair-round104-20261019/source` 本地 Git 中可读。将其七份 Go 文件的 blob ID 与当前副本 HEAD 对照，**七份完全一致**：

| 相对路径（前缀 go-server/internal/） | !140 与当前共同 blob |
| --- | --- |
| modules/dnf/disjoint/owner.go | 5561ec0458ceb959d2751d72286af2b4a0f64e3d |
| services/dnfbridge/disjoint_compound_owner.go | f499bb6a2094ebdb6a49c477020a0630ca83c7c0 |
| services/dnfbridge/disjoint_compound_protocol.go | b30959b97121f9b81c1db8d1d060ecac3ee2cba3 |
| services/dnfbridge/gameplay_module_equipment_disjoint.go | f4c8fc45f5f972bfa257c661eee3a0c3e49964f9 |
| services/dnfbridge/gameplay_module_avatar_disjoint.go | adb36f58acfc7c65a88d9f9b0732d646929bf91c |
| services/dnfbridge/delilah_cost_test.go | 4c52e69d215e331ac8f9a4eff69e1f05bb58c44c |
| services/dnfbridge/delilah_wallet_test.go | 6b03cee34585ef376f287f56954a1db3e375d5c0 |

当前历史 `460dcc17ce62f71f03480a78ebfc55e1d1254da2`（2026-10-20 22:54 +0800，restore cumulative gameplay）明确加入这套收费逻辑，当前 HEAD 已含该累计内容。未合并到上游 master 不等于未进入此累计基线。

`E:/90/repair-round104-20261019/delilah-published.json` 记录相同head、deployed=true、acceptance_environment=R93、user_accepted=false。`E:/90/交接文档-20261019-v141-R88.md:31` 另记录当时部署源码60f2289b、binary c34f0ff7…、15项线上检查及待实机。它们是历史回执，不用于证明今天生产进程仍运行该二进制。本轮未读取生产或部署。分类应为：**上游开放候选、历史曾部署、当前独立累计源码已保留、当前实机待核实**。

## 费用来源和资源

本次真实 PVF 为 `D:/codex-build-r129/container-baseline/Script.pvf`，已在前项核验 SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。

既有 `TestDelilahA56IdentityAndPVFHasNoFeeField` 在本次指定PVF上通过：npc/npc.lst 中1000→delilah.npc，名称德利拉、simple shop1000、disjoint item0；etc/disjoint.etc 未命中该测试列举的费用字段 disjoint cost/fee/price、gold cost、fee。测试名保留A56，但本次实际加载的是上述25675f…文件，不能把本次资源写成a56。

500是已存在的客户端契约常量 `disjoint_compound_owner.go:18–32`，不是由分解产物表算出。历史依据：

- `E:/90/repair-round104-20261019/source/Docs/DELILAH_DISJOINT_FEE_20261019.md`。
- `E:/90/repair-round102-20261019/delilah-cost/evidence/client_fee_branch.json`：v141 SHA26abf551…，image base0x400000；RVA0x2f0e31e比较NPC1000，0x2f0e34c选DSTR72992。
- 历史报告：RVA0x2f1b42a/434读取NPC对象及ID，0x2f1b43e调用op26 sender RVA0x21f57c0；RVA0x21f5c02写出context。旧报告称提示每次500，且v133三个窗口相同。本轮引用这些历史证据，没有新读二进制、没有独立复核v150这些地址。

真实物品回归使用既有传奇分解样本101020542，资源 `equipment/character/swordman/weapon/club/101020542.equ`，断言真实rarity6、分解后10099775灵魂进入账号仓库槽364。没有修改费用、PVF或玩家资产。

## 入口、事务、持久化和下行链

以下路径相对副本 `go-server/internal/`。

1. `services/dnfbridge/gameplay_module_equipment_disjoint.go:16–40` 注册 class1/op26 upper 与 legacy 路由，进入 `handleCurrentDisjointItem:44`。单件context位于业务体[5:9]，解析见 `disjoint_compound_protocol.go:376`；批量共享prefix携带context，经单独解码器校验。
2. `disjoint_compound_owner.go:35` 对context1000要求当前账号、角色匹配已完成且Scene.Cleared的副本runtime。context缺失或非1000继续历史分解路径；不能仅凭“人在通关场景”将其他入口强收500。
3. `commitCurrentDisjoint:162` / `commitCurrentDisjointBatch:228` 先验证装备及PVF产物，然后 `currentDelilahDisjointFee:28` 计算件数×500，`chargeCurrentDisjointGold:55` 从 Character.Stats[gold] 扣减，不使用个人机器回退钱包。
4. `modules/dnf/disjoint/owner.go` 在同一 `WithinAccountCharacterAssets` 内保存 CharacterFieldStats、InventoryFieldSlots、账号仓库；不足余额或后续失败均不提交。MySQL实现 `repository/mysql/mysql_account_inventory_transaction.go:72–150` 使用同一SQL事务、锁定读、错误Rollback、成功Commit。
5. 真实MySQL存储映射：`repository/mysql/mysql_char.go:57` 将 gold 映射到角色表gold列；`:369–429` 在 CharacterFieldStats dirty 时更新列并保存补充字段。不是只改内存后忘记持久化。**本轮运行测试使用内存事务；MySQL部分为静态代码核对，未连接DB验证。**
6. 单件 `gameplay_module_equipment_disjoint.go:315–322` 在提交后发op26 ACK，再发class0/op14钱包；批量`:200` 在ACK/槽刷新之后发一次钱包。`current_item_update_protocol.go:5` 构造主背包list0、slot0、itemID0的金币绝对数量行，零余额同样下发。发送失败不会把已提交业务伪装为业务拒绝。
7. 玩家分解机是独立CMD235路由：`gameplay_module_expert_job.go:134`、`expert_job_store.go:986/1070`，按store.Cost经currentExpertJobSettleStoreFee结算给机主、PlanDisjointer处理经验及耐久（约1121–1183）。本轮未改这些文件或规则，也未把普通玩家机器认作免费NPC。

已知边界：legacy单件测试使用现有“9字节业务体+4字节传输尾”契约；紧凑批量无此尾。当前legacy normalizer按长度剥尾，无尾的9字节legacy单件存在形状歧义，但本轮没有现场证据证明它是合法当前线路，故不改协议、不把该推测定为P133根因。context为0的旧通路本来不收费；缺少真实请求时不能证明用户现场请求携带1000。

## 有界验证

新增 `services/dnfbridge/delilah_dispatch_real_pvf_test.go`，测试 `TestRealPVFR134DelilahDispatchWallet`：

- 三条真实调用入口：handleCurrentDisjointItem、构造完整upper包调用handleGameUpper、构造完整legacy包调用handleLegacyGamePacket。
- 每入口7项：单件、两件批量、恰好余额、单件不足、批量不足、普通context0、未通关。共21子用例。
- 成功验证持久化金币、装备消耗、无关装备不变、个人机器兼容钱包0:0不变、真实PVF灵魂产物、最终一次金币刷新；再次提交原请求必须失败，角色/背包/账号仓库完全不变。
- 拒绝验证角色、背包、账号仓库完整记录不变、不发成功刷新。测试余额仅为隔离内存边界，非玩家DB修写。
- 同跑9项既有目标父测试，包含普通NPC317/无context、事务晚保存失败回滚、归属限制、个人钱包配置隔离、提交后发送失败等。总输出 **10父测试+31子用例PASS、0FAIL、0SKIP**（新增21子用例包含在31内），包耗时2.766s。
- 不配置PVF时新增测试SKIP，包PASS（0.060s）；明确配置时加载失败会失败。
- 当前已有修复直接通过，没有业务红灯。未人为删除收费制造回归，也未重新实现已有补丁。未执行全量suite、玩家分解机经验专项、数据库或原生游戏验证。

从go-server执行的目标命令：

```powershell
$env:GOMAXPROCS='3'
$env:GOFLAGS='-mod=readonly -p=2'
$env:GOCACHE='D:/codex-build-r104/main/cache'
$env:DNFBRIDGE_REAL_PVF_SMOKE='D:/codex-build-r129/container-baseline/Script.pvf'
& '<go-toolchain>/bin/go.exe' test -buildvcs=false -count=1 -run '^(TestRealPVFR134DelilahDispatchWallet|TestDelilah.*|TestDisjointFeeScopeKeepsOrdinaryMachinesFree)$' -v ./internal/services/dnfbridge
```

## 交付与现场下一步

本轮diff只有新增 `go-server/internal/services/dnfbridge/delilah_dispatch_real_pvf_test.go`、本报告和changlog新增一节。P67测试和报告SHA保持不变；未改 `gameplay_module_expert_job.go` 或任何 `expert_job*_test.go`。未提交、部署、创建MR、启动游戏或修改EXE/PVF/玩家数据。

本地证据位于 `runtime/r134-delilah-evidence/`：current-baseline.txt、default-skip.txt、mr140-live-metadata.json、mr140-live-changes.json、mr140-blob-comparison.json、manifest.json、r134-p133-tests-docs.patch。补丁只包含本轮增量，changlog以前一轮P67完成态为base。

主代理现场复核可利用已有 `game-disjoint-item-committed` / `game-disjoint-item-batch-committed` 日志的context和fee，结合实际运行身份、操作前后角色gold及class0/op14钱包。若context1000且fee500、DB已扣但UI不变，应沿下行/原生显示检查；若context不是1000，应先取得并核对真实入口契约；若线上代码不含该补丁，则核对构建/部署身份。当前证据不支持直接扩大收费范围、改玩家分解机收费或经验逻辑。
