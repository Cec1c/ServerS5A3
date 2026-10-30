# R134 P67 黑暗武士六项 TP 学习核验

> 本文环境地址、账号和工具链路径已脱敏；示例地址不表示当前部署目标。

日期：2026-10-21（香港）。独立副本：`D:/codex-build-r134/skill-source`。

## 结论与交付范围

当前副本和指定真实 PVF 在合法学习条件下，六项 TP 均可以学习。目标回归 **91 个子用例通过、0 失败、0 跳过**。因此本轮不修改学习业务代码，不提供未经复现支撑的“修复”。新增测试及本文记录当前可验证行为，不能据此宣称 test74 的现场问题已解决。

本次仅在独立副本新增测试、报告并更新 changlog；未修改 source/source-continuation、生产、客户端、PVF、玩家数据或其他职业免费技能逻辑，未提交、部署、启动游戏。内存角色 65001 / r134-pvf-fixture 与 test74 无关；测试等级从 PVF 最低等级派生，点数从 PVF 表加载，前置通过学习 handler 逐级购买。负例的不足 TP 数值仅用于内存边界断言，不是数据库修复方案。

## 输入身份

- HEAD：`7ca5fe751560118883416833edecad09182da40e`。用户说明此副本已准备依赖；本报告不独立背书每项依赖的发布身份。
- 问题文本：`E:/90/repair-round134-20261021/doc-audit/paragraphs.json:2428`，P67 前半，10.18 23:30，角色黑暗武士 test74，列举六项 TP 无法学习。后半活动入口闪退不在本次范围。
- 原文档元数据：`H:/YKCN90工作区/已知问题收集2 (1).docx`，SHA256 `91caa92b1fbc535178cd72f7f61c1a537999ee9a94d507bf05bb5208d96421f5`。
- 真实 PVF：`D:/codex-build-r129/container-baseline/Script.pvf`，SHA256 `25675fba8711216b72800e5ff034df61209b37e7c22d3e4c3b3ad7b1906fc348`。
- 已读根目录 AGENTS.md / changlog.md。按用户明确限制只运行目标测试，未执行 AGENTS 中的全套构建、全量测试或部署预检。

## 六项资源与学习契约

所有行均为 **job9**，资源前缀为 `skill/demonicswordman/`。共同字段：最低等级65、MaximumLevel=7、FeatureSkillType=1、被动、非 FixedLevelSkill、SpecialPurchaseCost=[2]；直接前置须达到10级。分支0上限5，按现有等级计算65级可学1级，69级可学5级；未提供第二觉醒最大等级表。

| 文档技能 | ID | 资源后缀 | 前置 ID / 等级 | grow maximum table | fitness |
| --- | --- | --- | --- | --- | --- |
| 强化-破魂斩 | 154 | drawswordexp.skl | 9 / 10 | [5 5 0 0 0 5] | [1] |
| 强化-灭魂封魔斩 | 158 | darkraveexp.skl | 79 / 10 | [5 0 0 5 0 0] | [3] |
| 强化-禁魂斩 | 226 | momentaryslashexp.skl | 60 / 10 | [5 0 5 0 0 0] | [2] |
| 强化-暗劲爆发 | 166 | darkexplosionexp.skl | 24 / 10 | [5 0 0 5 0 1] | [3] |
| 强化-魔影剑舞 | 217 | illusionslashexp.skl | 73 / 10 | [5 5 0 0 0 0] | [1] |
| 强化-黑暗冥王阵（文档用字） | 214 | wavespinexp.skl | 74 / 10 | [5 0 0 0 5 0] | [4] |

ID214 在真实 PVF 的名称是 **强化 - 黑暗明王阵**；本报告按职业、ID和资源路径核对，不把文档用字当作另一技能。

fitness 中虽不列0，但 grow maximum table 的第0项明确为5。当前 `SupportsCharacterGrowth` 以最大等级表作为职业成长准入，不把 fitness 元数据作为硬限制。packed grow=0/16/32 均落在低位分支0，高位觉醒阶段在没有第二表时回落既有表。测试这些值仅证明分支0行为，不断言 test74 当前 grow 或等级。

## 当前代码与入口审查

以下位置均相对独立副本根目录，未更改这些业务文件。

| 位置 | 核对内容 |
| --- | --- |
| go-server/internal/modules/dnf/skill/types.go:80、103、139 | fitness 与学习准入分离；SupportsCharacterGrowth 使用 MaxLevelForGrowType；MaxLearnableLevel 同时限制职业上限和角色等级。 |
| go-server/internal/modules/dnf/skillcmd/handler.go:23、194、358 | op29 解码并创建 buy command，经 owner 事务后组 ACK；不足点数返回2，等级/成长/前置等返回18，缺少技能等默认3。 |
| go-server/internal/modules/dnf/skillcmd/owner.go:435 | ApplyBuy 校验角色归属、当前技能树、job、持久化 grow、有效等级及事务。 |
| 同文件:624、627、644、657、662、694、1319 | 成长/固定技能/等级上限、TP与SP账本、最终前置校验均保留。 |
| go-server/internal/services/dnfbridge/game_dispatch_upper.go:11、509 | upper 包解析后，命令进入统一 aligned 分派。测试实际调用 handleGameUpper。 |
| go-server/internal/services/dnfbridge/game_dispatch_legacy.go:8、49、66、152、402 | legacy 原始头解析、body 归一化、已知学习尾部剥离，最终转 aligned。测试从 normalizeLegacyGameBody 后进入 handleGameCommandRequest，**不覆盖原始 legacy 头解析器**。 |
| go-server/internal/services/dnfbridge/gameplay_module_skill.go | 注册学习命令的 legacy 归一化逻辑；没有另一套放宽职业/前置的学习 owner。 |
| go-server/internal/services/dnfbridge/game_aligned.go:53、154、162、836、843 | 统一 handler；skillOwnerRules 提供实际职业目录、初始技能和PVF点数。PVP/dojo使用各自仓库及在场学习限制，未改动；新增回归未覆盖这些场景。 |
| go-server/internal/services/dnfbridge/initial_pvf.go:148、228、331、437 | 初始技能、技能目录、点数及等级点数目标来自 PVF。 |
| go-server/internal/services/dnfbridge/skill_slot_wire.go | 成功学习回复进行存储槽到客户端槽映射；测试解析 upper ACK 时校验并去掉 plain codec 三字节前缀。 |

可用历史中 `252287f`（snapshot: active container source 1ff735ba）的 types.go 已包含上述 SupportsCharacterGrowth 行为。历史只足以证明快照已有此逻辑，不能据此定位最初修复提交、部署时间或 test74 当时运行版本。

## 测试结果及可复跑命令

新增 `go-server/internal/services/dnfbridge/dark_knight_tp_learning_real_pvf_test.go:24`，主测试名 `TestRealPVFR134DarkKnightTPLearning`。

- 54项正例：6技能 × 3入口（owner-handler、upper、legacy-normalized）× grow0/16/32。购买首级后持久化等级1，TP准确扣2，SP不变；在最低等级65继续购买第二级均拒绝，整个技能记录不变。
- 36项负例：每技能覆盖缺前置、前置仅9级、等级64、PVF禁用分支、TP不足、其他职业目录无此ID。通过真实失败ACK及完整技能记录不变断言，负例走 owner-handler。
- 1项批量：upper入口一次学习六项，六项均持久化等级1，准确扣12 TP、SP不变。
- 总计91个子用例（连同父测试输出为92个 PASS 节点）。真实 PVF 结果：`runtime/r134-skill-evidence/baseline-r2.txt`，包耗时3.125s，0skip。
- 默认无环境变量：`runtime/r134-skill-evidence/default-skip.txt`，父测试 SKIP，包 PASS（0.057s）；显式配置 PVF 后加载失败会 Fatal，防止专项悄悄跳过。
- `runtime/r134-skill-evidence/pvf-rules.txt` 是初步只读抽取的字段/原文证据，提取用临时测试已移除；最终保留测试自身也输出并断言六项契约。

初轮 `current-baseline.txt` 在测试夹具调试中中断：存在 ACK codec 前缀处理、预期失败码以及外职业夹具错误沿用 job9 初始技能的问题。该日志**不是业务红灯，不构成复现证据**。修正测试夹具后的 baseline-r2 才是有效结果。没有人为修改业务规则制造红灯，故本轮没有“业务先红后修绿”的声明。

PowerShell，从副本 go-server 目录执行（环境变量只影响本次测试进程及其子进程）：

```powershell
$env:GOMAXPROCS = '3'
$env:GOFLAGS = '-mod=readonly -p=2'
$env:GOCACHE = 'D:/codex-build-r104/main/cache'
$env:DNFBRIDGE_REAL_PVF_SMOKE = 'D:/codex-build-r129/container-baseline/Script.pvf'
& '<go-toolchain>/bin/go.exe' test -buildvcs=false -count=1 -run '^TestRealPVFR134DarkKnightTPLearning$' -v ./internal/services/dnfbridge
# 默认跳过验证：先移除上面的 PVF 环境变量，再运行相同目标命令。
```

已对新增 Go 文件执行 gofmt。未运行全量 suite、其他职业专项、生产环境验证或客户端实机学习。

## 现场仍需的证据

未取得 test74 当前持久化 job/grow、等级、选用技能树、六项前置实际等级、剩余 TP、学习请求/ACK 或当时服务端身份；也未审查客户端是否在请求发出前阻止点击。不能在缺少这些信息时推断为服务器职业门禁错误或替玩家补等级/技能/点数。

下一步应由主代理在已有授权范围内核对上述只读状态，并区分客户端未发送 op29、服务端解码拒绝、owner 拒绝或成功后显示未刷新。本测试提供可对照的 PVF 契约和失败码，不建议增加免费技能、绕过成长/前置或修写玩家数据。

## Diff 文件清单

1. `go-server/internal/services/dnfbridge/dark_knight_tp_learning_real_pvf_test.go`：新增有界真实 PVF 回归。
2. `Docs/R134-P67-dark-knight-tp.md`：本报告。
3. `changlog.md`：新增仅测试/审查记录。

补丁包：`runtime/r134-skill-evidence/r134-p67-tests-docs.patch`；身份及结果摘要：`runtime/r134-skill-evidence/manifest.json`。runtime 证据属于本地交付，不进入生产构建。
