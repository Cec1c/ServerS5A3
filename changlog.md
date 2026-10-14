## 2026-10-14 — 主线任务经验（DOC-121）两位数 [difficulty] 档位修复候选

- 真档依据：`n_quest/questParameter.etc` 的 `[difficulty]` 同时声明单字符档（字母、`1`..`9`）与两位数档（`10`..`17`，如 `12`=600、`17`=734），因此难度是**字符串键**而非单 rune。扫描真实 PVF 的 8920 个带 `[difficulty]` 的任务，其中 378 个（全为 `[epic]` 主线：安徒恩85、镜像贝希摩斯58-59、卢克87-88、时空裂缝88 等）写的是两位数档。
- 根因：`progression/quest_experience.go` 的 `difficultyWeight` 是 `map[rune]int`，parse 用 `utf8.DecodeRuneInString` 直接跳过长度大于 1 的键；`quest/catalog.go` 的 `PlanFinishReward` 也只在 `len([]rune(definition.Difficulty))==1` 时把 PVF 难度投影到 plan，否则保留默认 `G`(30)。这些主线因此静默退回 `G` 权重。
- 旧失败/新通过（真实 PVF，同一只读探针）：任务 3481 `[difficulty] 10` 基线 `planDiff='G'` exp=251546，候选 `planDiff="10"` exp=417127（+65.8%）；任务 3926 `[difficulty] 17` 基线 `planDiff='G'` exp=756620，候选 `planDiff="17"` exp=1273766（+68.4%）。单字符档 3149 `[difficulty] 1` 两版均为 exp=6680，行为不变。
- 修复：`difficultyWeight` 改为 `map[string]int`，parse 以整 token（大写归一，兼容数据里的大小写混用）为键；`QuestExperience`/`FinishRewardPlan.Difficulty` 改为 `string`，`PlanFinishReward` 直接投影大写后的整 token，仅当 PVF 未写时才回退 `G`。改动限于 quest 与 progression 两个包的难度键，不动等级门槛、罚则、经验表与金币表。
- 回归：新增 `TestQuestExperienceUsesMultiCharacterDifficultyKey`（合成 `10`/`17` 键位）、`TestRealScriptPVFMainQuestTwoDigitDifficultyIsHonored`（真实 PVF 3149/3481/3482/3926/3927），并在既有 `TestRealScriptPVFQuestExperienceUsesVendorPermilleFormula` 增加 `10`/`17` 权重断言与 `difficulty=10` 用例。
- 边界：①参考实现 C# `ComputeExp` 形参是 `char`；本修复按 PVF 自身定义的两位数键取整 token，若厂商运行时实为首字符语义需另证，未在无厂商源码/原生读数前下结论。②相邻发现未修、非本项范围：真实 `[gold reward table]` 是 200 项平铺表，parse 却按 `value -1` 成对、步长 2 读取，`goldByLevel` 只有 100 项且等级错位（金币另案）。未部署、未改数据库/PVF/客户端。

## 2026-10-14 — 服务端与本地 GM 边界验证

- 干净检出验证：核心9293通过节点、858跳过、零失败；固定真实PVF 143个指定回归根全过（656通过节点、2未选中包）；gofmt/list/vet、Windows/Linux构建通过。BAT预检缺源码包未含的deploy/assets/channel_info.etc，未计部署通过。
- 原GM文件保留本地，独立本地工具不进入在线仓库；CI边界正反对照通过。

## 2026-10-14 — 按用户要求将在线仓库与本地 GM 工具分离

- 在线版本移除 GM Web 工具包及嵌入服务端的启动/停止绑定；本地原始 GM 文件和用户正在使用的工程全部保留。隔离清理树采用取消 Git 跟踪，未删除本地工具源码。
- 控制器不再生成 GM 环境配置，doctor 不再要求 GM 端口随游戏服务器启动。旧 gmListen 配置兼容读取后忽略；服务端游戏及通用管理接口保持原职责。
- 添加忽略规则、仓库边界检查及协作规范，防止后续 GM 更新重新进入在线仓库。验证与在线清理结果后补。

## 2026-10-14 — Reconcile MR !4 with the merged gameplay fixes

- Merge master 61036f9 into the independent D33 draft, retaining both histories and all existing changelog evidence. Normalize this changelog's line endings to avoid duplicating shared history during reconciliation.
- Preserve independent command-result snapshots for authoritative list refreshes and committed stackable-consumption receipts together; neither result may alias the handler's mutable storage. Add a regression covering handler reuse and consumer mutation with both fields populated.
- Keep exact-instance removal for equipment recipe upgrades, then consume remaining materials through the shared account-warehouse-aware recipe consumer. Preserve transaction rollback and inherited equipment attributes.
- Validate the resolved source: gofmt, go list, go vet, Windows and Linux builds pass; core tests record 9778 passed nodes, 876 skipped nodes and zero failures. The pinned real-PVF run passes all 137 required regression roots, recording 648 passed nodes, two packages with no selected tests and zero failures.
- The isolated BAT preflight stops because deploy/assets/channel_info.etc is absent from the source archive. This run does not establish deployment or native-client acceptance; retain the existing draft and open native validation items.

## 2026-10-14 — 已部署候选的原生验收与独立草稿同步（920）

- 919组合源完成9479通过节点、854环境跳过、0失败；固定PVF473通过、0跳过、0失败，gofmt/list/vet、双平台构建和实际BAT预检通过。Linux候选22a67ab64a0fb8b01b9a1685f22145eb44a16b585efb0080b919d2780a49ab56已部署，健康检查与部署前后完整采集资产比对通过。本条及验收文档只更新证据，所有Go文件与919验证快照逐字节相同。
- P38三种原生操作及新进程冷登录通过：主副手实例、强化、品级和锻造2/4与持久化结果对应。两张陈旧绑定截图已剔除，不作为通过证据。锻造显示成立依赖918 mode1 state120补齐，不把早先单独op14补发误写为已解决。
- 四件90A→90B按真实设计图完成升级，逐次与累计资产严格比对、完整Extra和119字节实例保留通过。新进程中鞋100%、腰54%、肩100%、裤49%正常；上衣仍为90A对照100%，两孔为空，已有徽章继承及第五件升级尚待验收。
- 自有角色账号仓库合并和单件拆分取出后，未重选角色，金币6000992、共享晶块1000及五件品级持续正确。两次成功ACK之后均有权威列表发送事件；往返资产除两处更新时间外逐项相同，对照角色保持。此前918金币显示0的失败截图及纠错均保留。P61/P68请求空格但自动合并另一格的原生端点覆盖仍未完成。
- P33/P39/追加9城镇跨角色持久卸武器与P56空白导航的客户端修复已另行完成验证并发布client-v99-d33-912；客户端补丁不在本服务端MR。上衣徽章与第五次升级、P61/P68重定向端点、P23合法已开启团本一阶段原生验收仍保留开放项；普通卢克解锁仍取消，MR !2继续由作者处理。
- 原定时迁移对话已停止，全部电脑操作由主代理承担。三个本轮离线子代理已关闭。独立MR保持Draft，尚不合并；当前测试通过不代表尚未覆盖的原生场景已完成。详见Docs/D33_NATIVE_VALIDATION_20261014.md。

## 2026-10-14 — 金库与整理后的钱包、品级刷新候选（919，待验证）

- 918已部署。P38原生单主手、单副手、双武器中心按钮交换及完整新进程冷登录均完成，强化、品级、锻造2/4与实例保存对应；原MR !2继续由作者处理。
- 自有106原生金库合并及单次取出后金币显示为0、背包装备品级变成数量1对应的低品级；只读SQL仍保留6000992金币及原品级种子。重新选角后金币和品级恢复，明确是操作后的刷新错误。918金库截图文字中“金币正常”已纠正，P61/P68不据此关闭。
- 对物品移动实际发出的主背包整容器刷新，标记由桥侧复用会话权威投影，保留钱包、共享材料、品级和孔位。主背包整理沿用相同处理；ACK端点与先后顺序保持。读取权威钱包失败时复用现有跳过刷新机制，不发缺钱包快照。
- 新增真实inventory handler到bridge下行的五场景回归，覆盖账号金库直接合并、自动合并、拆分取出、个人金库自动合并与主背包整理；已在旧代码重现缺钱包、品级被写成1、孔位丢失。候选完整验证与部署后原生复验尚待完成，不能宣称已修复上线。

## 2026-10-14 — 原生附魔及四件90B升级验收；冷登录锻造候选（918，待完整验证）

- 已部署916的原生附魔复验完成：自有106四次附魔与四次打孔均收到成功回包，金币6000992、共享金色晶块5000及肩部最上级100%显示保留。完整SQL核对附魔、双空孔与原实例属性；在线奖励490006576自动增加1件另有事件日志，不算附魔资产差异。
- 主代理以原生设计图完成鞋、腰、肩、裤90A→90B四次升级，每次op25成功ACK及committed日志、逐次完整资产快照均保存。界面剩余精炼时空石10、魔岩石34、金色晶块1000；四件强化、增幅、附魔与双空孔显示已观察。精确实例差分仍在校验，上衣与已有徽章继承样本、冷重登仍待验，不能据空孔证明镶嵌徽章继承。
- 916中心按钮交换后的第二次完整冷重登显示主手+7/100%、副手+9/26%，槽位与SQL一致；锻造2/4仍未显示。op14补发不作为修复通过证据。
- 为mode1装备创建行的state120增加已归一raw[0x57]回退，保留显式mode1_state_120/state_120（包括0）的优先级。通过真实仓储到mode1生产构建入口重现旧代码三组锻造4/2均投影0的失败，新增八个回归子用例限制只有两处锻造字节改变；修复后的测试、组合检查及原生显示仍须独立完成。
- 当前线上仍916 b5954891…，918候选未部署或同步MR；MR !2保持作者处理，电脑只由主代理操作。

## 2026-10-14 — 冷重登主副手槽位与锻造交付、附魔钱包失败边界（916）

- 自有角色119实机中心按钮已完成单主手、单副手及两把不同属性光剑交换；SQL保存了交换后的槽位和实例，但完整退出重登后UI按旧槽位标记还原，锻造2/4均未显示。此问题仍待修复后原生复验，不能凭SQL一致关闭。
- 主副手12/24互换时同步当前槽位标记、0x77原始行槽位及已有十六进制镜像，保留其余实例数据和事务出口。桥侧对有运行时移动标记的12/24行优先采用持久化SlotIndex，使已受影响存档无需直接改库即可还原；未移动的初始装备保留旧槽位转换。
- 冷登录场景完成并发送装备容器后，沿用已有op14行更新补发带0x57..0x59尾部状态的装备；只读权威仓储，成功发送后记录事件，可用DNFBRIDGE_LOGIN_EQUIPMENT_TAIL_REASSERT=off回退。主代理添加真实登录边界回归，保留未接入补发时的失败证据；实际锻造显示仍须客户端验收。
- 附魔完整主背包刷新必须包含同次构建的权威钱包行；角色金币读取失败或缺失时跳过该刷新，保留已提交附魔的ACK。未标记操作保留既有行为。
- 主代理完成槽位、冷登录边界和钱包读取失败的先失败后通过检查，正在执行组合完整验证。915候选已单独完整验证并保留备份，但未部署；916检查不沿用915结果。MR !2继续由作者处理。

## 2026-10-14 — 附魔成功后的主背包完整刷新（915，待部署验收）

- 自有角色106真实附魔后，客户端金币6000992显示为0、金色小晶块5000显示为0、上衣品级100%显示为12%；只读数据库确认资产和品级种子均未改变。op272成功前置的class0/op13整表刷新只使用角色物品快照，遗漏钱包和账号共享格，并将装备品级编码为数量1。
- 成功的非宠物附魔结果标明需要完整主背包投影；桥侧在原响应位置复用会话物品列表构建器，沿用原信封及刷新先于ACK的顺序，保留钱包、账号共享格和装备实例字段。投影不可用则跳过该刷新并保留已提交操作的ACK。其他未标记操作和宠物附魔路径保持原行为。
- 主代理保留代理原始交付并修正测试字符串及未使用导入，先在旧发送逻辑上重现三项失败，再确认同组五项回归通过。完整检查和真实客户端复验尚未完成，本条不代表已部署或已关闭问题；原MR !2仍由作者处理。

## 2026-10-13 — D33 掌握技能审计按职业判读（871）

- 保留 `[required job skill]` 的职业标签与技能编号配对，仅用本次技能页所属职业的要求判读。技能编号在不同职业中复用，不能将其他职业的同编号技能当作本职业的掌握技能。
- 固定 PVF 的原代码回归已重现两处误判：job11 仅下发33且缺少本职业要求95、job0 仅下发95且缺少本职业要求33，均曾错误记录为 all_requirements_served。新增真实 PVF 跨职业回归，并保留本职业掌握已下发的成功对照。
- 不支持的职业记 skipped；无法完整解析的职业/技能配对记为未完成审计，避免把未知状态记录为全部满足。此改动仅修正技能页下发处的日志判读，不改变协议字节、仓储、卸装策略或装备规则，不能作为 P33 城镇跨角色卸武器已修复的证据。
- 此节对应本地独立源码后续修正；截至开始验证时，MR !4 仍为已验证提交 0a5bf3c4，本地871尚未提交、部署或原生验收。完整验证及同步结果另行记录，旧 CI 数字不覆盖本地改动。原 MR !2 留给作者自行修复。

## 2026-10-13 — D33 独立已知问题修复候选（待原生验收）

- 基于主线 f30f50a5，仅纳入设计图属性继承升级、剑豪中心按钮、金库自动堆叠实际落点 ACK 和卢克奖励画面防重修复。
- 设计图确定性升级覆盖 90 张配方及全部 25 张卢克 90B 防具，其他 197 张属性继承配方仍保留原有拒绝条件；原生属性、扣料及重登效果待验。
- 原 MR !2 留给作者自行修复；本候选不继承其账号工具、主线/转职、幸运星或金币罐改动。历史组合候选测试不作为此独立版本的验证结果，主代理重新验证。
- P56 空白网页导航仍在客户端定位中，本次没有客户端 EXE 或资源改动；香港 2026-10-14 02:00 后再执行游戏操作。
- 登记 2026-10-13 20:40 高优先级城镇跨角色卸武器反馈，关联 P33/P39/追加9；新增掌握技能武器触发条件；用户确认受影响角色重登后仍未装备，按持久化状态变化优先定位，尚未修复。
- 草稿源码尚未合并、部署或发布客户端 Release。
## 2026-10-14 — Preserve equipment quality after sorting

- Use the same durable quality-seed precedence for inventory refreshes and login: dedicated metadata, equipment-only legacy aliases, then a matching complete current-client row. Sorting no longer replaces quality with the equipment stack count.
- Keep legacy quality aliases out of the independent item-state field. Preserve persisted item instances and stackable quantities; do not generate replacement quality or change assets.
- Regression coverage includes first/repeated sorting, bag/personal cargo, ordinary/top quality, legacy and raw-row sources, quantity isolation and invalid/foreign raw data. The new handler regression fails before the repair and passes afterward. Native in-game quality display remains pending acceptance.
## 2026-10-14 — Use current equipment definitions for ordinary clear cards

- Build a separate ordinary-card equipment pool from list-backed PVF minimum level, rarity and positive creation rate. Legacy dictionary gaps no longer substitute crafting recipes for high-level equipment; monster and abyss policies remain unchanged.
- Preserve existing rarity rolls and level windows, exclude cosmetic/special equipment, honor original/revised asset precedence and reject incomplete scans. Stream and warm the cached pool during startup to avoid a first-clear scan delay.
- Regression coverage reproduces the former recipe fallback, checks high-level candidates, weights, level limits, special/disabled items and failed scans. Optional real-PVF sampling verifies varied equipment at low and high levels. Native card display/claim remains pending acceptance.
## 2026-10-14 — Preserve legendary souls in equipment disassembly

- Read disassembly soul rows as counted PVF records, retaining the disabled chronicle row and its constant placeholder. Legendary equipment now uses its own rarity row instead of a truncated or shifted entry.
- Reject malformed or partial soul tables before consuming equipment. Preserve the existing reward quantities, item transactions and account soul-warehouse projection; no asset or balance rewrite is required.
- Regression coverage includes the real-PVF missing-soul failure, all rarity rows, malformed tables, source-item preservation on failure and committed warehouse rewards matching the acknowledgement. Native disassembly display remains pending acceptance.
## 2026-10-14 — Restore gold-cap upgrade purchases

- Handle the current client's upgrade request using its verified 14-byte request and stage/result reply. Charge the advertised 5,000,000 gold once per stage, require level 60 and sequential stages, and stop at stage four.
- Persist the debit and purchased stage in one character asset transaction. Reject foreign owners, malformed requests, insufficient funds and corrupt stages; a duplicate request repairs the client's stage and wallet without another charge.
- Publish the saved stage during actor initialization, including zero on character changes. Existing reward, trade and auction policies are unchanged; this repair restores the purchase and the native client's cap selection.
- Regression coverage includes both transports, concurrent duplicates, transaction rollback, native failure messages and login ordering. An opt-in disposable MySQL test checks reload and rollback across the wallet and supplementary stat tables. Native UI acceptance remains pending.
## 2026-10-14 — Consume profession crafting materials from their durable owners

- Reuse the existing recipe material projector for profession crafting, including enchanter products and card-to-bead crafting. Shared crystals and souls are debited from the account warehouse; ordinary materials remain in the character bag. Historical character copies cannot substitute for account balances.
- Persist warehouse changes in the same settlement transaction as materials, products, gold and profession experience. Preserve recipe requirements, success rates and the verified request/reply layouts; refresh the committed container balances after acknowledgement.
- Matching-PVF regression reproduces the prior insufficient-material rejection and covers all eleven enchanter product recipes, batch quantities, bead metadata, wrong/locked/insufficient shared rows, foreign ownership and late transaction rollback. Existing equipment-recipe behavior is retained. Native crafting UI acceptance remains pending.
## 2026-10-14 — Charge lottery jar opening prices atomically

- Read explicit lottery gold prices independently from NPC acquisition materials. Paid hero jars no longer require their exchange token a second time. Preserve explicit opening materials and the existing material-only legacy jar policy; reject malformed prices instead of treating them as free.
- Settle gold, source jars and rewards in one account/character asset transaction. Inventory-only magic-box entrypoints reject paid jars; lottery and booster entrypoints refresh the committed wallet after success.
- Regression coverage includes matching-PVF hero and legacy jars, complete reward references, both lottery reply layouts, duplicate confirmations, insufficient gold, full bags, foreign ownership and rollback after all writes. Disposable MySQL validation covers competing opens, durable reload and rollback on a late database error. Native opening animation and reward display remain pending acceptance.
## 2026-10-14 — Cover Grandine room transitions with passive drops

- Add matching-PVF coverage for both ordinary Grandine routes, including the fifth-room exit. Before the passive-drop allocator fix, actors and drops shared identifiers and a later room was rejected for reusing an earlier drop key.
- Assert scene identity separation, allocator progress, stable room rebuilds and arrival at the boss. The previously introduced allocator repair covers these routes without additional runtime changes; native gameplay acceptance remains separate.

## 2026-10-14 — Restore Gust Zone boss-room transitions

- Accept repeated identical passive-item group definitions across maze declarations without multiplying rewards. Continue rejecting malformed or conflicting definitions.
- Reserve planned actor identifiers before allocating passive drops, and retain the highest allocator position when committing a room transition. Preserve shared party allocation and prepared drops across transport retries.
- Regression coverage includes conflicting definitions, solo/shared identifier allocation and matching-PVF traversal of both ordinary Gust Zone boss routes. Verify the existing start-map payload, distinct actor/drop identifiers, failed-write retry and exactly-once pickup. Native in-game traversal remains pending acceptance.
## 2026-10-14 — Avoid repeated friend online notifications after room loading

- Keep the last successfully delivered adventure-friend projection for each selected character generation. Unchanged room-loading and presence refreshes no longer resend the native online-notification packet; changed rows and pending invitations still refresh normally.
- New selections and connections bootstrap their lists again. Commit the projection only after successful information writes, before sending pending invitations, so a pending-list retry does not replay an online notification.
- Regression coverage includes repeated finish-loading, offline/online changes, notes, character switching, reconnects, renamed/deleted rows and transport failures. Current-client disassembly confirms the repeated-notification branch; native UI acceptance remains pending.
## 2026-10-14 — Give adventure-shop purchases their PVF use periods

- Resolve a product's relative PVF use period at purchase time before granting it. Adventure-shop potions now receive a durable expiration date instead of a zero deadline; use the same purchase clock for the grant and account settlement.
- Preserve permanent items, reject unrepresentable deadlines before charging, and retain the existing account/inventory transaction. Existing instances are not assigned invented purchase dates or renewed by later purchases.
- Regression coverage includes the matching-PVF seven-day potion, inventory refresh fields, relogin projection, separate purchase deadlines, insufficient points, full inventory, foreign ownership and late transaction rollback. Native tooltip and use acceptance remain pending.
## 2026-10-14 — Serialize dungeon lucky-star awards with account wallet changes

- Settle each lucky-star award inside the existing rental account/character transaction instead of an unprotected account read/overwrite. Concurrent awards and wallet operations now share the account-row lock.
- Validate the selected character's account ownership before writing and retain the suitability rules, cap and per-run replay guard. Transaction failures remain retryable without reporting an award or changing account metadata.
- A deterministic regression reproduces two successful awards losing one increment. Coverage also includes ownership, missing transaction support, rollback and the existing settlement replay/notification-failure paths. A disposable MySQL check verifies eight concurrent awards alongside a gold purchase, durable reload and late rollback. No protocol, schema or historical balance changes.

## 2026-10-14 — Prevent duplicate lucky stars on settlement replay

- Remember when a completed run's lucky-star award has been resolved. Exact settlement retries no longer grant additional stars or resend gain notices, including after a best-effort notification write fails.
- Resolve capped awards as well, so spending later cannot make an old clear grantable again. Repository failures remain retryable and a fresh dungeon run retains its own award.
- Regression tests reproduce repeated grants before the change and cover duplicate requests, account-save failure, notice failure, caps and unsuitable levels. This does not change lucky-star purchase protocols or establish native UI acceptance.
## 2026-10-14 — Restore equipment-based catalyst crafting

- Accept complete PVF equipment recipe predicates instead of rejecting every recipe without an explicit item-ID list. Check type, part, rarity, level range, one-item instances and sealing capability; incomplete recipes remain rejected. Sealed and previously unsealed resealable weapons follow the same PVF eligibility rule.
- Use the PVF minimum/maximum output counts and bounded per-unit maximum-output probability, retaining the existing atomic material/output settlement and reply format. Validate probability ranges and abort on random-source errors.
- Regression coverage includes matching-PVF weapon selection, material rejection, account soul costs, spent-input replay, ownership/locks, output probability boundaries and transaction rollback. Native crafting animation acceptance remains pending.

## 2026-10-14 — Charge fixed catalyst ingredients for every crafting unit

- Scale each recipe's mandatory ingredient by the completed crafting-unit count, including carried contribution. Batch crafting can no longer produce many catalysts for one unit's fixed material cost; output multipliers remain independent.
- Preserve the account/character asset transaction and committed balance refresh. Refuse insufficient stock and arithmetic overflow without consuming contribution materials, changing carry or granting products.
- Regression coverage includes the matching-PVF dimensional catalyst, single/batch account and bag costs, carry, output multipliers and rollback. Native crafting animation acceptance remains separate.
## 2026-10-14 — 按 PVF 阵营识别怪物型友方 NPC 的击杀贡献

- 友方协战贡献同时覆盖 AI 角色和原生怪物两种对象类型；怪物型贡献者必须是已宣告存活、绑定匹配地图行且被 PVF [monster team] 显式设为玩家阵营的对象。跨房跟随沿用本次副本来源房间的身份与阵营证据，不以 NPC、地图或技能 ID 放行。
- 保留当前房间目标、未知类型、敌对/未宣告/退役对象及教程边界；拒绝将友方对象本身作为此兼容路径的击杀目标。重复死亡和发送失败重试仅补发已提交的死亡通知。
- 新增针对性失败回归及匹配 PVF 的怪物型协战全链路验证：五个房间共 23 次击杀、重复请求不重复结算并进入第六房；既有 AI 协战回归保持通过。画面与真实玩家复测仍单独验收。

## 2026-10-14 — 修复友好 APC 击杀后怪物残留、房间无法清空

- 兼容当前官方客户端的死亡报文：击杀归属为 `0xffff` 时，允许携带服务端已宣告、仍存活的友好 APC 战斗贡献项。继续验证当前房间的普通怪物绑定；未知/敌对/未宣告 APC、跨房目标、教程及旧协议不因此放宽。
- 复用既有死亡、经验、掉落与清房流程；重复死亡或通知写入失败后的重试仅补发权威死亡通知，不重新结算击杀奖励。保留跨房跟随 APC 的原对象标识。
- 增加原生报文边界、拒绝路径、重复结算及传输失败回归，并用匹配 PVF 重建阿甘左协战房间，验证六只阻挡怪死亡及后续过图。游戏内画面效果仍需实机验收。

## 2026-10-13 — Count merged NPC purchases by this purchase's quantity

- Own normal QA account10071/character108 bought two talismans490010214 on server703. Native inventory merged to slot3/count2, gold fell from1000992 to600992, and read-only SQL retained count2/week2962. Full normal client restart716 preserved the inventory, gold and expiry and displayed2/70. The live second purchase displayed3/70 and shop reopen retained3, so instantaneous display remained a real defect.
- Current signed client c3ea63ec's BuyItem handler VA0x1FD80E0 reads the item quantity and passes it to the additive purchase counter at0x1FD8E51/0x1C61450. Project stackable BuyItem ACK entries using this purchase's quantity; keep equipment instance bytes, stored RawEntry and following class0/op14 inventory totals unchanged. Apply the same projection to material exchanges and retain consumed-material balances.
- Private validation724 preserves three baseline failures and passes the same three regressions plus related NPC-shop checks with the fixed PVF. Required combined validation and native post-deployment verification are still pending. This change does not reset quotas or alter prices, expiry normalization or repository transaction rules.
- P2/P37/P50 remain excluded; ordinary Luke unlock remains cancelled. Raid/reward private candidates remain outside this release. Their independent reviews still identify open recovery and collision boundaries.

## 2026-10-13 — Deploy validated expiration merge and verify the own-account restart

- Source970aaf5f0ef7c20e67cc556170b381690ad909e0 passed all four jobs in pipeline165161: Linux571828, Windows571829, core571831 and Semgrep571830. Semgrep itself succeeded; its allow-failure setting is not used as evidence of a pass. Shipping audit710 verifies all seven required expiration tests and the exact committed3475-file source snapshot against validation699.
- Deployment703 installed the validated Linux SHA25681172a8de42e830a1823e6394fa3ba605a6e33380073513b3edeedbf20073226 with backup /opt/dnf90/backups/d33-resume-970aaf5f-20261013-703. Independent704 confirms PID1177881, zero automatic restarts, ready and118 game listeners. Configuration, PVF, signed v97 client feeds and relay are retained. Own-account selected assets match the pre-deployment snapshot; SQL707 still reads purchase count70/week2962 without writes.
- Normal full own-client login706 starts PID32656 and returns character106 to the activity shop. Native display remains70/70, gold6000992 and existing materials remain. Read-only708 verifies the live counter70 and enabled715 bit without memory writes, pause or injection. This is deployment/relogin preservation evidence; the existing capped account cannot yet supply a new successful purchase to demonstrate native expiration merging. No quota reset or historical row consolidation was performed.
- Maintenance source705 matches the validated3475 files after backing up the previous3471; runtime settings are retained. Four private raid/reward candidates continue, with unavailable-read unknown-commit callers and full trace persistence still open. P2/P37/P50 remain excluded and ordinary Luke unlock remains cancelled.

## 2026-10-13 — Merge purchased stacks using their persisted expiration

- Normalize retired absolute PVF expiration dates before the shared product grant compares an existing row and its stack identity. Repeated purchases now compare against the same renewed date written to inventory. Future absolute dates, daily-delete expiry, distinct legitimate instance expiry and transaction boundaries retain their existing rules.
- Preserve the private unmodified-source RED evidence for the second purchase and the 70-item row count. Consolidate seven shipping regressions, including a post-mutation repository Save fault that leaves inventory, gold and account purchase count unchanged. Parent combined validation699 passed: default8813 passing nodes/830 environment skips/0 failures, fixed-PVF240 passing nodes/0 skips/0 failures with all required tests present, gofmt,210-package list, scoped vet, Windows/Linux builds and actual BAT preflight. Source3475 files stayed byte-identical through validation. Deployment is not yet performed.
- Record completed P29 native evidence: own account10070/character106 purchased to70, actual attempt71 returned opcode21 error0x85, shop reopen and full client restart retained70. Earlier capacity error0x04 at56 was resolved by normal sale of QA items. This establishes the tested purchase-count boundary; the new expiration merge is a separate server change awaiting deployment and native verification.
- Client delivery695 published signed preview/stable version24.9.12.42-v97-d33-715. The only EXE change is opcode715's allow bit at file offset111747929 (VA0x6E92359): 0x00 to0x08. New EXE SHA256c3ea63ecbc8df88697aba1e3b0c3314ff6fbdf4a6d0a6c21cbe04f70e1700056; old82e0c6a392387ed428a39770f89bc67754ee208b5caa81c6d3c959d11c2e7c7c remains in rollback. Protocol profile90cn-decode-bypass-v1, PVF369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06 and dstr ee8904c8540ef63807dc984604a3ea0f5cc4040228e5ac2e95d490c03d21ec08 are retained. Independent postpub695 verifies shipped UpdateEngine.Fetch/Check and the actual signed blob; all other7711 manifest rows are unchanged.
- P2/P37/P50 stay excluded; ordinary Luke unlock remains cancelled. Private raid recovery and reward candidates are not included in this change. Earlier dated statements below retain their historical scope.

## 2026-10-13 — Project one top-level row per friend adventure group

- Project each accepted friend account once. Prefer its durable last-selected character while online; otherwise choose the least online character ID using a total order. Preserve the existing offline fallback, row fields, pending relations and 255-row batching.
- Keep the existing projection admission/error behavior. The representative helper only orders admitted candidates; it adds no rejection gate. Numeric ties have a stable spelling tie-break.
- Preserve decoded 1373 deletion followed by 1371 upsert and 1372 completion on representative changes. Current formal-client handlers 1371/1373 use account-friend manager 0x56A3774; character-friend 291 uses separate manager 0x56A37D0.
- Candidate baseline target-rule RED and candidate regression receipts are preserved under p57-group-projection591. Parent validation641 passed: gofmt, 210-package list, default core, fixed-PVF related tests, scoped vet, Windows/Linux builds and actual BAT preflight. Audit646 confirms every new group test passed; skipped tests remain skips. Native populated-friend grouping and the small red-box control remain open; this source change is not full P57 acceptance and is not deployed.
- P29 independent own-account read626 proves count1/week2962 persists; ack627/639 sends item490010214 count1 in list0. Native display remains0/70 and current own client cache reads635/637 find no key in either counter. Continue actual opcode715 consumer/codec investigation; do not attribute this to lost persistence or Tuesday reset.

## 2026-10-13 自主继续工作检查点618（香港 2026-10-13T06:20:15.064345+08:00）

容器修复ad246477cf1ac12d965de33d15b2e200f8b0ee82已完成597联合验证、165066四项云CI success、608标准服部署及609独立复核。线上PID1166774，自动重启0，二进制SHA2561d303d010d0ee607ac24b5ca48209b6b5344b293a9e40daa91b30c002fcd53d6，118游戏监听，配置/PVF/更新清单/relay保留。回滚备份/opt/dnf90/backups/d33-resume-ad246477-20261013-608。612备份旧3468文件维护源码并同步3471文件，四个路径变化，运行配置未改。团本571私有候选未部署。

既有自测10070/106正常退出、正式启动器重新登录并重选入城；609只读完整选定资产快照等于部署前，金币19800992、417总5600、3277×7、490010214×1主栏121保留。没有重置账号/SQL写或重复发放。

P56原验收重点是页面关闭与商城退出回城；about:blank允许但非必须，op773不是关闭验收门槛。正式客户端82e0c6a3/当时线上4d7b1b2b的583和606两轮充值X关闭、黑钻确认关闭、商城退出回城均成功，无闪退；此自测范围通过。保留about:blank未出现的旧证据，不用此现象否定关闭成功，也不扩张到其他客户端版本。

P29出现真实新失败：购买后及同会话重开1/70，在服务重启、重新登录/重选后实际显示0/70，金币和已购物品仍保留。此次恰跨周二06:00，周策略应周四06:00；实际count请求shop_id3136/body_len4，不能归因查询用了itemshop ID。尚未取得持久化两键，桶差异、完整Account.Save覆盖、Redis缓存或客户端投影原因仍在查，不宣称购次已持久化或数据库确已归零。70/71原生边界仍待验收。

P61/P68新版本个人金库8格打开，600堆叠双击与拖动未产生op19，不能计存入通过。P57原生三页空占位行数角色好友7/冒险团好友6/黑名单6，仍见小三角；冒险团好友页没有自身角色行，已纠正此前空列表/自身行预测，真实好友分组和按钮功能未验收。

P2/P37/P50保持排除，四个现有代理无子代继续。团本571g单人恢复5例私有通过，父独审要求补配置/布局/清关journal篡改与真实非零进度；Aquinas事务门与窗口测试通过但真实生产handler失败ACK/全hook仍未完成。C盘闲置Go缓存清理约28GiB完成，活动缓存/模块/工具链保留，06:17附近可用25056137216字节（约23.3GiB），编译自测持续。整项任务ACTIVE，历史失败保留。

证据：container-cloud-ci603-c/receipt603.json、resume-standard-deployment608/receipt608-deploy.json、container-deployment-verification609/receipt609.json、maintenance-source-sync612/receipt612.json、native-qa-account574/native-mall606-observed.json、native-qa-account574/own-shop-events614-shop614.json、native-qa-account574/social611/。以下旧段落均为各自时点事实。

## 2026-10-13 自主继续工作检查点600（香港 2026-10-13T05:09:40.699077+08:00）

用户已明确授权使用自测账号并自主完成后续工作，四个现有子代理继续并发，P2/P37/P50退出修复队列，已验收关闭项保持关闭。

容器初始化及保存失败控制已完成597联合验证：默认8805 pass节点/825 skip节点/0 fail，固定PVF相关233 pass节点/0 skip/0 fail，required专项齐全。gofmt、list210包、scoped vet、Windows/Linux构建及真实BAT check --skip-database全部通过。源码3471文件验证前后哈希相符。本批尚未部署，当前线上仍4d7b1b2b；跳过与历史全PVF失败、超时分别保留，不能计为通过。

自测账号10070/角色106 qa57590级继续原生验收。正常任务5578“急切的青龙”和“被破坏的祥瑞溪谷”均通过对话完成，正常金币奖励共992；第二任务ID只读回读待补。随后正常进入祥瑞溪谷，打开海流商店，购买490010214一张，单价200000，客户端周限由0/70变为1/70。只读回读确认物品主栏121数量1、expireTime1830268800、金币19800992。没有GM完成任务或重置购次。70/71边界、重开及重选持久化仍待验收。

P66已见真实op20消费品按类型整理（category2/condition3/response_count2），417×600从主栏66移到65，整理时钱包20000000保持一致；没有独立op13日志，晶体按钮路径仍开放。P61/P68金库打开成功但拖动未见op19，不能计存入。P56两页打开关闭及两次op65确认，about:blank效果和op773仍未通过。原生邮件已领取417总5600、3277×7；发放未重复，无其他玩家改动或外部支付。

团本571b资源摘要/恢复占用补丁冻结，571c真实活会话截止回城4 PASS，移除重绑分支的行为负例1 FAIL；完整Settings恢复和所有生产持久化接线仍在四代理候选区，未混入共享源码。C盘清理约28GiB已完成，当前主Go缓存、模块及工具链保留，验证继续通过。

证据：container-combined-validation597/receipt597.json、continuation-checkpoint600/native600.json、native-qa-account574/own-events583-epicroad598.json、native-self-test543/gm543-purchase598check.json。以下旧段落均为各自时点历史。

## 2026-10-13 — Initialize missing move-container state and preserve honest native acceptance

- Before routing a valid selected-character move between known character containers, establish missing main/avatar/cargo state through the settings owner, then read it back. Existing valid state stays unchanged; load or save failures reject before inventory mutation or ACK. Wrong class, malformed requests and unrelated containers retain their original guards.
- Preserve baseline failures and verify the integrated owner/handler controls: inventory 543 and related bridge 118 passing nodes, plus four passing save-failure test nodes without skips. Scoped vet and build passed. Full combined validation and deployment of this new batch remain pending.
- Continue native testing under the user's own-QA authorization. Normal login, creation, town entry, virtual gold and mail claims are confirmed for the QA character. Mall pages open and close, but about:blank is not accepted; no op773 was observed. Empty-tab sort and vault drag attempts do not prove op13/op19 acceptance. Keep P2/P37/P50 excluded and all unverified cases open.

## 2026-10-13 — Verify all four cloud jobs for the deployed resumed source

- Independently GET pipeline165000 and jobs for source4d7b1b2b: Linux571472, Windows571473, core-tests571475 and Semgrep571474 all succeeded on Runner1615 with complete timestamps. Semgrep itself succeeded despite its allow_failure configuration.
- Preserve previous CI/resource failures and keep native acceptance separate. No private job trace was read or exported.

## 2026-10-13 — Deploy and independently verify the resumed batch

- Publish source4d7b1b2b after validating identical committed bytes; deploy its Linux artifact with a verified backup and rollback path. Independently confirm process1159493, zero restarts, ready, binary SHA25692ab4a57 and all118 game listeners.
- Preserve runtime configuration, resource PVF, client feeds and relay. Match formal v96 to both feeds; synchronize3468 validated source files to the maintenance tree after backing up its previous3452 files.
- Record default8791 pass/824 skip nodes and related actual-PVF218 pass/0 skip nodes, scoped vet and both builds. Preserve initial resource-allocation failure and prior full-PVF failures; native acceptance remains pending after02:00.

## 2026-10-13 — Preserve account-vault stack identities and known PVF caps

- Compare the repository's explicit stack identity fields before merging, while retaining normalized PVF contract comparisons. Reject conflicting stackable type and expiration metadata.
- Reconcile a missing cap with the matching item's recorded cap and preserve that cap on the merged destination without mutating caller snapshots. Explicit conflicting caps still reject. Mail construction now records the positive resource cap.
- Preserve red baseline and default missing-PVF failures. The final actual-resource mail/deposit and identity controls pass 13 cases without skips; default resource-dependent controls explicitly skip when no PVF is provided. Native P61/P68 acceptance remains pending.

## 2026-10-13 — Suppress ground items for admitted team raid runs

- Freeze team mode on the legal raid entry and suppress six ground-item creation routes for all admitted participants, including expired/retired entries. Ordinary and single raid drops retain their positive controls.
- Integrate only the verified six production files and three tests. Preserve and reject the earlier mixed comparison patch; preserve full real-PVF timeout and baseline resource failures separately.
- Luke Light Altar quest essence grants are a separate direct-inventory route; extra7 and native player acceptance remain open.

## 2026-10-13 — Load sort-wallet projection before committing inventory

- Independently reproduce the first gold candidate's transient-read boundary: it sorted and persisted inventory before failing its later Character load and reporting the request blocked.
- Load and clamp the authoritative wallet before owner.Sort. A read error now rejects before any inventory write or ACK; after commit only the prepared value is used. Keep optional state and non-wallet slot conflicts unchanged.
- Reject the proposed fallback that would send a full main-list refresh without the wallet on read failure. Preserve both earlier candidates and their failure evidence; native crystal-button acceptance remains pending.

## 2026-10-13 — Enforce resource-declared NPC purchase windows

- Read the current three-token purchase-limit form, including the real Epic Road account weekly70 policy for490010214, and retain the legacy two-token form's cumulative/daily-marker behavior.
- Correct the first candidate's explicit-daily reset gap: an explicit daily period uses the existing06:00 service day without requiring a separate reset marker. Weekly uses the server's existing Thursday06:00 operating policy; the weekday is not a PVF fact.
- Retain transaction quota consumption, cross-character account scope, concurrency and rollback controls. Both the original omission and the first-candidate daily defect have preserved RED/GREEN evidence. Native purchase/acceptance and deployment remain pending.

## 2026-10-13 — Correct native mall web-ticket replies

- Match the current op773 consumer's success/type/string-length/string/code fields, correcting the old6-byte short reply. Supply the user-authorized about:blank address in op773/op65 with the proved ASCII length limits.
- Keep failure replies, ticket request limits and the registered handlers intact. The protocol mismatch has static and RED/GREEN evidence; the effect on subsequent parsing and the player's inability to close the mall still requires native acceptance.
- Integrate only four web-ticket production/test files after a clean patch check. Candidate bridge/vet passed; combined-source validation and deployment remain pending. Do not close P56 from an offline reply test.

## 2026-10-13 — Preserve authoritative gold during inventory sort refresh

- Reproduce four missing/stale wallet failures through the real Sort handler and native full-list response. Character.Stats retains4321 while the old response omits the wallet or emits9; the prior516 fixture already contained the wallet and did not cover this projection gap.
- Project Character.Stats gold into a cloned list0 snapshot, matching login limits and preserving non-wallet slot conflicts, absent optional state and other containers. Do not persist a synthetic gold inventory row.
- Full inventory package, projection boundary controls and merged controller/protocol retry pass. Preserve531's initial startup timeout; GOTELEMETRY=off is used for the completed retry. The exact player crystal-button request and native acceptance remain unconfirmed; P66 stays open. Deployment has not occurred.

## 2026-10-13 — Resume four repair lanes with updated player scope

- Resume D33 at the user's direction, exclude player issues P2/P37/P50 from repair, and preserve accepted closures and the canceled ordinary Luke sequence work.
- Assign isolated account-vault, team-ground-drop, mall-close and Epic Road limit candidates. Permit native testing and test item/attribute grants after Hong Kong 02:00; before that continue source and offline validation only.
- Remove raw provider stderr from repository synchronization failures and add synthetic git/CLI error-output regression checks while preserving operation and exit metadata.

## 2026-10-13 — Verify the merged master pipeline on the restored cloud Runner

- Independently read pipeline164976 at2026-10-13 00:30:45 Hong Kong: exact b181abd1 source, all four Linux/Windows/core-tests/Semgrep jobs success on Runner1615, complete timestamps, runner online and unpaused.
- Preserve historical path-test failures and the user-requested stop. The game service remains18393; the merged Go source and pending game candidates are not deployed by this documentation update.

## 2026-10-13 — Stop the current section and preserve an acceptance handover

- Publish the current standard-server retest scopes and remaining reports after explicit player acceptance; retain P66 gold loss separately and stop superseded ordinary Luke sequence work.
- Fast-forward the local repository to the player's merged MR1 b181abd1. Keep the running game at validated18393 and the maintenance Go tree unchanged; repository merge is not deployment.
- Restore cloud Runner1615 and execute actual Linux/Windows builds and SAST. Preserve old core-test failures and the running merged-master verification pipeline rather than declaring full CI success prematurely.
- Save the four workers' completed evidence and unfinished steps, pause the02:00 test heartbeat at the user's stop request, and record restart conditions. No game/SQL/client operation or local Docker installation occurs.

## 2026-10-12 — Reconcile explicit player acceptance and stop superseded work

- Close P80 forest main-quest progression from the player's additional acceptance; retain unrelated activity admission issues.
- Close the player's accepted volcano barrier, raid return, waiting-area access, training admission, skill-option persistence, sound and awakening-reset reports. Preserve earlier accepted results and distinguish the new crystal-sort gold loss from the accepted crystal display.
- Accept mechanical-throne prerequisites by the reported behavior, retain the P18/P19 numbering alias, and stop unshipped sequential-unlock candidates at the user's explicit direction. Keep the unrelated light/dark reconnect report separate.
- Accept the current adventure-friend character display. Investigate the small button from actual client evidence, replacing the unproved collapse/expand assumption. Reassign the same four agents; no runtime, client or database operation occurs in this record.

## 2026-10-12 — Deploy the validated source to the actual standard test server

- Apply the user's new direction: the existing online service is exclusively for testing, replacing the earlier keep-r98 policy. Use the standard launcher channel for acceptance; both update channels launch the same live game service.
- Deploy the exact validated18393b1d Linux amd64 artifact and3452-file public source archive to the existing dnf90.service. Independently verify the new PID, running binary, readiness,118 game listeners and bootstrap/auth/GM ownership. Retain the actual prior binary backup for rollback.
- Keep the client manifests, PVF, effective configuration and relay state byte-identical. No GUI or manual SQL operation occurs. Publish only the completed Anton town-return and activity-origin subscopes for retesting; keep all reported broader failures and accepted closures accurate.

## 2026-10-12 — Preserve owned completed Anton returns and ordinary Seria origins

- Freeze an internal return owner on legal normal Anton map entry and record only locally authenticated map completion before global clear guards. Allow completed town return after another squad, timer or phase changes global progress; preserve the existing reward, clear-count and retry/selector boundaries.
- Exclude the complete actual PVF town28 Epic Road identity from ordinary Seria origins, preserving town50 behavior, ordinary saved origins and existing room-door fallback. Do not claim the player's native target or full relogin/Abis behavior is proved.
- Validate the exact combined3452-file source: all DNF domains/bridge/control8712 pass nodes,0 fail,783 environment skips; fixed-PVF155 pass,0 fail,0 skip; required vet and Windows/Linux amd64 server builds exit0. Preserve the initial missing-root-template control failure and repair only its exact public fixture before the full control rerun. No runtime deployment or GUI acceptance occurs.

## 2026-10-12 — Correct deployment provenance and acceptance wording

- Freshly verify the running isolated server PID, startup time and binary hash. Its installed 3443 source files exactly match20b0c911; the published3449-file tree only adds controller/diagnostic changes relative to that instance.
- Clearly distinguish source candidates, repository publication, installation/startup and player acceptance. Keep402/398/409 unintegrated and undeployed. Do not attribute all failed player retests to missing updates: prior game changes present in the installed tree remain unresolved where the player reports failure.
- Retain the daily r98 deployment, four-worker limit, local Docker/Runner stop and native test pause until Hong Kong2026-10-13 02:00. This checkpoint changes documentation and state only.

## 2026-10-12 — Record ordinary Luke sequence and Seria bottom-door regressions

- Keep team-raid ground loot prohibition separate from ordinary Luke map unlocking and the two independent Light Altar boss quest grants. Remove the previous throne/essence subscopes from the current candidate queue after the player's failed retest.
- Accept only the Epic Road NPC return to Seria. Keep the bottom door still reaching Auspicious Valley, cold relogin and Abis admission unresolved. Record nine candidate and49 open scope groups while retaining every earlier accepted closure.
- Retain source-only four-worker assignments and native pause until Hong Kong2026-10-13 02:00. Preserve417's failed push and418's fresh old remote head instead of claiming publication.

## 2026-10-12 — Review Linux/remote deployment merge request against current master

- Inspect MR1 source ca254089 in an isolated object store. Its exact combined tree merges cleanly with f894304 and passes protocol/bridge/control regression, deployment and sync tests, scoped vet and Windows/Linux server builds. Preserve environment skips and unexecuted Docker/native checks.
- Prepare a bounded synchronization error-output correction with synthetic RED/GREEN and all seven synchronization integration tests passing; recommend it before merge. Source pipeline details return403, so keep its failure cause unconfirmed. No online merge, runtime change, Runner registration or local Docker operation occurs.

## 2026-10-12 — Reconcile latest player regressions and accepted sound setting

- Close P55 from explicit player acceptance. Retain P56 click-crash acceptance as partial while mall closing remains unresolved; the player permits a blank-page route. Move the nine failed scopes out of the current acceptance-candidate queue without deleting prior source-test evidence.
- Prioritize Anton reference comparison and in-dungeon progress/other-squad return failures. Expand P20 from material filtering to every floor item and gold drop in team raids. Preserve free-fatigue, skill-lock/CD selection, account-vault stacking, crystal sorting and automatic first-awakening reports as unresolved.
- Keep the four-agent cap and the native pause until Hong Kong2026-10-13 02:00. This checkpoint records player evidence only; daily r98, installed QA and stopped local Docker/Runner remain unchanged.

## 2026-10-12 — Clarify P57 adventure-group friend layout scope

- Record the player's screenshot contract: top-level entries are adventure-group friends; characters of the same group belong to its expandable content. Keep the online-only and last-online-character fallback rules, but classify grouping and collapse/expand behavior as unresolved until the current-client consumer contract is verified.
- Preserve the native-test pause until Hong Kong2026-10-13 02:00, the four-agent cap, accepted items, daily r98 and stopped local Docker/Runner. This checkpoint changes documentation only; no protocol field, product source, runtime, database or client operation is introduced.

## 2026-10-12 — Pause native tests until02:00 and reconcile current acceptance scopes

- Honor the new Hong Kong2026-10-13 02:00 native-test deadline while continuing code work. Update the existing one-shot recovery, candidate scope list, unresolved original-player reports and accepted closures. Keep source/runtime/deployment distinctions and the unconfirmed357 docs push explicit.
- Preserve the failed independent worker evidence and replace the closed slot within the four-agent cap for offline review. Keep native launches, daily r98, local Docker and Runner operations stopped as directed.

## 2026-10-12 — Record normal Anton prerequisite completion and reconcile remote CI state

- Observe the second native role completing the supported Lv89 story sweep, ordinary Anton side quest12829 acceptance, native claim of100 golden cubes from one normal GM mail, and ordinary material submission/reward settlement. Authenticated readonly checks confirm12829/3779/3757 completed and all100 cubes consumed; before/after runtime identities remain intact. Keep normal channel admission and the whole-volcano acceptance open.
- Correct the earlier unconfirmed347 push reference with successful ordinary353 source commit1de3be47, and retain355's final canceled pipeline164782 with all four jobs unstarted and unassigned. Read failed-pipeline notifications disabled without claiming restored Runner or remote-CI success.
- Preserve installed QA20b0c911/3443, reviewed source3449, daily r98, accepted player items and the stopped local Docker route. Publish this evidence as a separate docs-only commit.

## 2026-10-12 — Add fixed raid-create rejection diagnostics and narrow QA prerequisites

- Add reviewed fixed rejection events at the current create handler and its outer op664 authentication dispatcher. Preserve authentication, short-circuit order, original failure frames, successful metadata-before-ACK and other commands; log only fixed stage enums and existing safe connection metadata.
- Integrate four exact production/test paths into repository and maintenance source, with3449 source/module/fixture entries matching the sealed candidate. Complete bridge default-core regression with5635 passing nodes,0 failures and654 retained environment skips. Build Windows amd64 and Linux amd64 servers; preserve the separate Windows386 server's existing constant-overflow failure and do not replace the field-tested386 client controller.
- Observe bounded unbound auth metadata from the immutable full capture. Identify the normal local launcher's15-minute in-memory token lifetime and the later channel-hop token-unbound event, without declaring canonical cause or changing token lifetime or raid gates. Record that a normal fresh process is required for a new launch token.
- Read the second role's actual quest prerequisites through normal authenticated GM GETs:12829,3779 and3757 are not started or acceptable, with no3262 inventory rows. Preserve normal quest settlement and all accepted player reports. Keep installed QA20b0c911, daily r98, whole-volcano GUI acceptance, stopped local Docker/Runner and remote-CI limits explicit.

## 2026-10-12 — Narrow ordinary raid-create failure and record second-role prerequisites

- Publish reviewed native startup source12650e3f and confirm failure-pipeline notifications disabled. Cancel only the new pipeline whose four jobs were unstarted and unassigned; keep Runner and local Docker stopped, with no remote-CI success claim.
- Validate the observed op664 legacy request/meta pair and native header privately; its route0, ordinary UI flags000, two-byte ASCII name and current length shape pass existing source gates. Observe one normal same-role reselect and same-name creation still rejected; preserve session-auth/runtime-membership uncertainty without lowering any gate.
- Observe the second role's ordinary Anton-channel prerequisite rejection. Identify the real PVF side quest12829, prerequisite3779 and material3262x100; record the interrupted source worker and unfinished normal-completion route instead of modifying quest data or claiming a raid formed.
- Preserve all previously accepted player items, daily r98, installed QA source20b0c911 and open whole-volcano animation/damage/kill/reset scope. New rejection diagnostics and bounded auth projection remain external work in progress.

## 2026-10-12 — Integrate owned native multi-instance startup and observe two roles

- Add the reviewed Windows386 controller route for the fixed native client: hold the new child identity, restrict single-instance call interception to declared native call sites, restore the startup gate, bind the normal authenticated PID and detach before the existing five-second check. Preserve first-client/server/database identities and explicit unsupported-architecture refusal.
- Integrate seven exact control production/test paths into repository and maintenance source. Confirm the complete3447-file source/module/fixture map, formatting, package listing, scoped vet and Windows amd64/386 plus Linux amd64 controller compilation. Reuse184 control/Doctor passes with4 expected skips from identical reviewed inputs; retain domain/bridge/PVF results at their original scope.
- Execute one independently reviewed external386-controller trial successfully and observe two native role windows. Keep installed/running isolated source20b0c911 unchanged; newly compiled controllers are not installed or executed. Default amd64 explicit multi remains unsupported.
- Observe a normal native raid-create failure and confirm source-declared accepted channel type23. Preserve complete logs and safe metadata without exporting packet bodies, credentials or original log digests; raid authentication/binding and the failure cause remain unproven. Keep ordinary volcano animation/damage/kill/reset acceptance open.
- Record native Epic NPC return and same-role reselect subscenes only. Preserve accepted player items, daily r98, the four-agent cap and stopped local Docker/Runner route. Do not claim restored remote CI or full original13 acceptance.

## 2026-10-12 — Observe native Epic NPC return and preserve unresolved volcano scope

- Handle the latest ten-minute GUI delay once after its13:31:27 Hong Kong deadline and a fresh identity check. Observe the ordinary isolated native NPC entry and return to Seria; keep relogin/town-exit and Abis admission open rather than closing original13.
- Preserve complete frozen service/packet inputs and the actual external after guard. The prior parser refused association; correct the new adapter's unsupported login-binding attribution and identify four duplicate grow_type diagnostics using source-declared event/key counts only. Keep new adapter and native multi-instance candidates outside deployment pending finite independent review.
- Retain validated/running Go source20b0c911, daily r98, accepted reports and the four-agent cap. Keep local Docker/Runner stopped and remote CI execution unproven while awaiting the user's cloud server.

## 2026-10-12 — Switch normal isolated QA and confirm native material delivery

- Complete the reviewed normal BAT/controller switch with private backups, keep the external database running, install the exact3443-file source map and verified programs, pass the real endpoint preflight and prove READY with121 TCP/3 initial UDP. Enable GM through the normal loopback instance configuration; independently verify the new process/state and retained database.
- Observe normal account login and native entry to Seria. Send one entry-material attachment through normal GM mail, claim it in the native mailbox, and independently confirm main-inventory count1 with the previously accepted necklace unchanged.
- Execute one new supported additional-client diagnostic using the integrated held-handle facts. Record genuine native exit1 during the5-second check, retain the first client and unknown inner cause, and preserve the sealed115 history. Ordinary two-member volcano damage-shield acceptance remains open.
- Keep daily r98 and accepted items, the four-agent limit, disabled project failure mail and stopped local Docker/Runner route. Remote CI has not executed; preserve all gates and await the user's cloud server.

## 2026-10-12 — Preserve native additional-client exit diagnostics and portable regression sources

- Capture liveness, true exit status including259, and100ns birth facts through the held Windows process handle before Release. Keep unknown facts explicit and retain the existing registration, environment,5-second guard and successful-start behavior. Do not infer the cause of the earlier live client failures.
- Integrate the four production files reviewed by202 and five permanent tests approved by214. Replace the rejected author-absolute formatter source with the current package source; assert the exact nil enum for each platform and reject fabricated zero exits or known birth facts.
- Verify the complete3443-file candidate with144 control/Doctor passes,0 failures and3 expected skips, gofmt/list/vet and Linux control-test compilation. Reuse209 Windows/Linux program builds after proving every non-test source file unchanged; compilation is not Linux runtime execution. Retain173 domain/bridge/scoped-PVF results at their original scope.
- Keep runtime QA on31e2b51/3433 source while preparing the normal GM switch. A fresh read-only check confirms the former client absent and no related DNF processes, with server and retained database identities unchanged. Preserve unknown exit cause, player acceptance conclusions, daily r98 and the stopped local Docker route.

## 2026-10-12 — Enable explicit normal loopback GM configuration and reject endpoint reuse

- Add optional server.gmListen, disabled when absent/blank. Require a valid127.0.0.1 port and reject numeric aliases of admin/channel endpoints. Generate GM environment values only through normal instance configuration; preserve inherited bridge-env scrubbing and default legacy behavior.
- Reject duplicate role endpoints in the standalone Doctor path too. Integrate exact independent161-v04 production files and independent181 permanent tests; make two Doctor regressions require the explicit collision error rather than passing on a previously occupied listener.
- Validate snapshot173 with9042 core passes and791 environment skips,396 scoped actual-PVF passes and one separate-asset skip, list/vet and all three Windows/Linux BAT builds. After the test-only correction, validate all control/Doctor tests with136 passes under shuffle, preserve actual v03 two-test RED, and vet the final changed packages. No further production or build change. Earlier broad real-PVF failures remain historical evidence.
- Preflight an independent runtime copy: normal check --skip-database rejects occupied QA endpoints; check additionally skipping ports passes assets/client/configuration. Do not label the ordinary preflight or remote CI passed. Keep running QA source31e2b51 and daily r98; optional GM has not been enabled in either running instance.
- Record the latest GUI pause to2026-10-12 10:44:03 Hong Kong, preserve accepted items, and continue four source lanes. Keep unsafe159/163 capture revisions rejected pending corrected independent reviews. Keep local Docker/Runner work stopped awaiting the user's cloud server.

## 2026-10-12 — Record real raid creation and bounded follow-up evidence

- Record an ordinary native UI raid creation after normal token-bound login: one actual role in phase0, insufficient entry material and no raid start. Supersede the pending manual-name assistance state.
- Execute the independently reviewed second-client115 preparation once. Preserve SECOND_CLIENT_EXITED_AT_5S_CHECK, the retained first client and the unknown inner cause; seal that run and do not retry it.
- Independently verify the original Epic-return rollback counterexample passes with both errors exposed. Keep the external candidate unintegrated while its native NPC request contract remains open; partial socket writes and failed persistence rollback remain explicit limits.
- Verify four actual-PVF Abis calendar/admission/preflight controls without finding a production contradiction or introducing a candidate. Retain native request and player-quota acceptance as open.
- Record the user-requested GUI pause until09:35 Hong Kong and one scheduled resume, while four source/review lanes continue. Keep Go source31e2b51, daily r98, closed acceptance items and the stopped local Docker route unchanged.

## 2026-10-12 — Stop the local Runner setup route at user request

- Record the completed per-user Docker Desktop installation and failed WSL VM engine starts separately. WSL3.0.1/default2 and Windows virtualization were already present. Do not claim a usable container engine or Runner.
- Stop Docker, verify Desktop/backend processes are absent, and disable its newly created current-user login startup entry. Retain the installed software and avoid a system reboot. No Runner was created or registered and no CI job started.
- Move the next Runner setup to the cloud server the user will provide. Preserve CI job definitions, notification preferences, running source31e2b51, daily r98 and player acceptance status.

## 2026-10-12 — Limit repeated CI failure emails and documentation pipelines

- Set this user's project notification level to custom with failed_pipeline=false, then read it back; leave global notification settings untouched. Keep other event fields at their existing/default values. Cancel the pending documentation pipeline164597 after verifying no job has started. Previously queued emails may still arrive.
- Add workflow rules that skip ordinary existing-branch pushes changing only root documentation. Preserve the complete Go subtree, build/CI/deploy inputs, tags, new branches and explicit pipeline entrypoints. Keep all existing build, core-test and SAST definitions intact; new build modules outside the watched paths must update the rules with their introduction.
- Validate the proposed configuration with the site's CI Lint and pipeline simulation. Both baseline and candidate simulations retain the same two builds, core-tests and semgrep-sast. Preserve the stricter static comparison's implicit-only normalization difference separately; this is configuration validation, not remote execution.
- Keep the unavailable Runner dependency explicit: the only visible instance Runner remains paused/offline/stale and no project Runner is available. This change reduces notifications and unnecessary queues; it does not make remote CI pass. Go source stays31e2b51 and daily r98 remains unchanged.

## 2026-10-12 — Bounded volcano flag evidence and open activity rules

- Record nine bounded fixed-client controls through conditional scene refresh, actor flag setters, real obfuscated storage/readback and attack-target qualification. Keep actual monster spawn, damage, HP and GUI acceptance open.
- Record the independently reviewed local second-client diagnostic preparation with explicit optimization rejection; it has not been executed against the game. Preserve the unknown cause of the earlier single failed normal CLI attempt.
- Reject the catalog-only joust draft after actual-resource transaction controls confirm material, participation reward and limit ownership gaps. Preserve old ledger behavior and leave the unproved new settlement rule open. Qualify the gold-cap source and native evidence without inventing fields or packets.
- Update the timestamped documentation pipeline snapshot. This commit changes documentation only; validated and running source stays31e2b51, daily r98 and accepted player reports remain unchanged.

## 2026-10-12 — Normal isolated QA live checkpoint and Runner failure

- Record the one normal controller start of source31e2 using the retained database, the read-only adoption after a case-only SHA guard correction, wrong-password rejection, and fresh native token-bound town entry.
- Preserve the original crafted necklace and saved512-byte option/skill data; keep skill GUI restoration and ordinary Anton0% animation/monster damage open. Record the failed additional-client attempt without inventing its cause or counting persisted roles as simultaneous online members.
- Keep the isolated joust2365 catalog mismatch open after persisting its existing disabled setting. Daily r98 and accepted player reports remain unchanged.
- Confirm pipeline164529 and source31e2 pipeline164570 failed with stuck_pending_no_matching_runners before scripts; current project Runner settings are enabled but the sole visible instance Runner is paused/offline/stale. Local combined56 validation does not replace remote CI execution. This update changes documentation only.

## 2026-10-12 — Reviewed normal native launch and character options replay

Integrate the current-Service username/password token route and consume its fresh token through normal launch-client. Keep PID registration as ownership metadata and retain existing raid/token/character guards. Login payloads and unknown provider/transport errors retain only bounded length/hash diagnostics; fixed domain-rule diagnostics remain readable. Preserve the independent nested-provider disclosure regression and the rejected round47 evidence.

Reset the character option replay gate on a real role entry and consume it only after successful op390 delivery, before op19. Reject non512 option saves only for the evidenced official24.9 profile. Preserve failed-write then fresh-reentry regression; same-entry retry and native GUI restoration remain open.

Support the verified official native/hookfalse profile through normal control. Add explicit start --use-running-database, default false, for the existing loopback QA database. This path creates only its own verified server process state, skips portable DB init/start/rollback/mysql.ini, closes schema initialization, and does not stop that external database. Existing owned DB lifecycle remains the default.

The exact combined56 source passes go list, 8560 core tests with774 environment skips, 396 scoped actual-PVF tests with one separate-asset skip, scoped vet and Windows/Linux BAT builds of all three programs. Log48 independent joint controls pass210/0/0; control50 independent tests and eight-file SHA audit pass. Mirror3433 source files. Earlier full-PVF failures remain historical evidence; this is source validation, not new GUI acceptance. Daily r98 remains unchanged; isolated native animation/damage, lock restoration and fresh token owner binding still need actual tests.

## 2026-10-12 — Review and integrate skill reset, Epic return and Luke waiting

- Rebuild free profession skills for op491, parse real automatic-level containers and reject manual mutations. Cover actual planner-produced class change/awakening, missing real grants, ownership and rollback; record the shared effect on73 automatic skills and recovery of historical points through full reset.
- Exclude the exact Epic Road resource from ordinary return origins; retain explicit compatibility mode and old snapshot limits. Bind the exact Luke waiting room to existing raid ownership without restricting ordinary area2.
- Withhold the durability tool proposal after independent review disproved its section classification and client-cause claim. Preserve P86/P87 and existing-data acceptance as open.
- Complete 8279 core and 388 scoped real-PVF passes, with 773 environment skips and one separate-asset skip; go list, scoped vet and Windows/Linux builds pass. Synchronize3403 source files and keep broader historical PVF failures visible.
- Preserve four actual worker deliveries/cross reviews and cap4. Record the isolated native raid-create failure and unbound-token evidence; running QA stays51886be and dailyr98 remains unchanged. No new GUI acceptance or public deployment.

## 2026-10-12 — Integrate four-worker repairs and GM request ordering

- Preserve accepted yellow records and original closed groups. Keep the four-subagent cap and record four actual deliveries; reuse lanes for disjoint next tasks.
- Merge account cargo auto-stack/count mirrors and atomic shared-crystal sort snapshots, exact training fatigue policy, actual quest tuple wildcards, character-scoped option saves and complete reward projection deduplication. Preserve GUI/SQL boundaries and broader historical PVF failures.
- Protect GM grow-options responses and errors by request generation and character identity. Retain male/female job values and labels; seven offline frontend scenarios pass after three baseline failures.
- Validate8266 core and332 scoped actual-PVF passes without failures, with740 environment skips and one separate-asset skip. Complete scoped vet and Windows/Linux builds; GM461 passes with17 skips. Preserve the failed output-decoding wrapper and occupied-QA preflight evidence.
- Synchronize3393 source files; running isolated QA stays frozen51886be, daily production staysr98. Volcano native animation/damage and remaining player acceptance stay open.

# Repository changelog

## 2026-10-12 — Tencent Linux remote test environment

- Add a separate Windows remote entrypoint using the inspected client, private WireGuard game address and loopback-only SSH bootstrap/account forwards. Preserve the existing local entrypoint, PVF/executable hash guards and API login tokens.
- Deploy the Linux image and MariaDB 11.8 to the 90cn host, with a dedicated persistent database volume, bounded container memory, VPN-before-Docker ordering, a systemd service and daily consistent logical backups. Copy the two existing local characters into the new remote database without replacing local data. Verify service recovery after an actual host reboot.
- Reproduce the remote v56 login failure: the unchanged client sends class1/op1 with a 610-byte body, routed into the legacy ignore path. Accept this observed exact length alongside 606 in stream and lifecycle classification; retain adjacent-length rejection and token binding. Regression cases fail before the change and pass afterward. The cause of the four-byte layout difference remains unproven.
- Verify actual remote token login, channel 10, the migrated roster, Lorain movement and a monster kill acknowledged with 39 experience, followed by town entry. Stop computer interaction at the user's request once connectivity is established. Full quest/party/raid acceptance remains separate; document validation and the legacy Windows preflight dependency in Docs/REMOTE_TEST.md.

## 2026-10-12 — Docker Linux profile and local token launcher

- Add a separate Linux image and generated Compose profile under ignored runtime/docker, with a dedicated MariaDB volume, private interface bindings, embedded UDP relay and timezone data. Extract real PVF channel assets with a source-built tool and preserve explicit external table overrides.
- Bind the embedded UDP relay to gameListenHost when configured while preserving the advertised host address and the legacy default; cover NAT address separation and invalid bind addresses.
- Add BAT/Python local-client entrypoints for the inspected v56 executable, API-issued login tokens, duplicate-client and asset-hash checks. Keep the vendor updater and binaries unchanged.
- Match the inspected v56 executable to the existing official-24.9 packet counts and recognize its observed 606-byte login body/token offset. Verify actual token-bound Linux game login, character creation, persisted roster after restarts, and keyboard movement in the Lorain tutorial; do not infer full gameplay acceptance.
- Add an opt-in restriction of default-group directory requests to real ordinary resident channels for clients that repeat bootstrap lookups without the script download. Retain multiple rows for native auto-selection. Enable it in the Docker local profile; retain named groups and the original server defaults/lifecycle. Revert an ineffective experimental keep-open change.
- Explicitly select the repository's existing channel reference for the tested v56 assets, retaining provenance and the PVF-extracted table. Keep the observed missing raid-map references and full gameplay verification open; no old one-click database or client binary patch is needed for this smoke test.
- Complete live fast-forward synchronization in both directions: GitHub Actions runs 38162071646 (GitHub to GitGud) and 38162153930 (GitGud to GitHub). Linux Verify passed after portable controller path fixes (38161846104).

## 2026-10-12 — Portable controller path validation

- GitHub's first Linux run reproduced two existing controller failures: backslash traversal was accepted in an installation receipt and Windows absolute assets bypassed the expected path guard.
- Normalize both separator styles and reject drive-relative/absolute Windows paths and UNC paths on every host. Retain Windows behavior and add cross-platform escape cases; do not skip the failing checks.

## 2026-10-12 — Optional GitHub / GitGud fork synchronization

- Add an opt-in GitHub Actions synchronizer for one master branch, using only fast-forward pushes and an independently scoped GitGud Deploy Key. Stop on divergent history or imported changes to CI definitions.
- Add a read-only verification workflow for Go checks, Linux/Windows builds and synchronization tests, including checks after token-driven synchronization.
- Validate seven integration cases with temporary bare repositories: equality, dry run, both directions, divergence, CI-change rejection and missing branches. Remote workflow results are recorded separately after publication.
## 2026-10-12 — Player report classification and gunner GM labels

- Classify72 source reports, close the five yellow acceptance records, preserve previous accepted groups and assign every remaining source record to an isolated lane. Record actual429 dispatch failures and the user-requested four-agent cap; resumed dispatch is not evidence of normal worker progress.
- Distinguish male/female gunner labels in GM choices after actual-PVF confirmation that both base names are identical while their awakening profiles differ. Preserve job values and keep the reported gender overwrite's full GUI cause open.
- Validate8230 core tests and461 GM tests without failures, scoped vet and Windows/Linux builds; synchronize3384 source files. Preserve prior PVF evidence and skips separately.
- Verify the original necklace remains visible in a fresh isolated client and read the same instance without granting/crafting. Retain formal-token verification failure and pending volcano damage acceptance. Daily r98 and the frozen51886be QA service are not replaced by the new GM build.

## 2026-10-12 — Isolated QA service switched to candidate33

- Shut down the exact owned QA09 service normally and retain the same database/config/assets. Preserve the initial port preflight block on the old admin connection's TIME_WAIT, with no candidate process created at that point.
- After endpoint release, start the frozen final32 candidate once; independently verify new-PID READY and118 game TCP listeners, then read the original necklace instance without granting/crafting again.
- Keep the player's standard client and dailyr98 untouched. New native PID binding and volcano animation/monster-damage acceptance remain open; source CI pending is separate from successful local builds.

## 2026-10-12 — Initial town snapshot deadline and preserved QA evidence

- Give the full initial-town player-state snapshot the existing15-second budget instead of the3-second mutation budget; preserve packet order, route/character guards and error withholding of typed op24. One legacy achievement cleanup transaction inherits the longer budget; other independently scoped contexts stay unchanged.
- Reproduce lost context after3.1 seconds with a real actor-route regression against the previous production file; verify healthy snapshot ordering and unchanged repository-error rejection after the patch. This is a unit-budget proof, not native/MySQL acceptance.
- Preserve cold29 disconnect/verify failure and cold30 early exit; retain the same crafted necklace by read-only snapshot without granting/crafting again. Standard launcher login is user-confirmed; volcano deployment and GUI acceptance remain open.
- Complete final32 domain/bridge/control, scoped actual-PVF, go list, scoped vet and Windows/Linux builds. Retain broader historical PVF failures, the separate-asset skip and the occupied-QA preflight boundary. Production staysr98.

## 2026-10-12 — Anton expired completed-map return across phases

- Preserve an already-owned first-phase expired-completed return receipt after the same Anton instance advances to the real second phase; retain exact run/account/channel/participant guards and town-only permission.
- Reproduce both op42/op72 failures before the patch with the real PVF phase VM, then cover phase advance, foreign instance rejection and unchanged retry/reward gates. Keep native phase-return root cause and GUI acceptance open.
- Complete final27 domain/bridge/control, scoped real-PVF, go list, scoped vet and Windows/Linux builds; preserve historical broader PVF failures and asset skip. Production stays r98 and QA09 retained.


## 2026-10-12 — Anton volcano native barrier state and adventure friend presence

- Derive native Anton symbol111 from full barrier depletion/recovery after verifying the shipped client controller. Keep it latched during partial recharge and restore it in reconnect snapshots; preserve ordered110/111/126 and110/111/127 writes without altering formal PVF markers. Exclude phase1 and Luke.
- Verify unmodified Go-exported break/recovery frames through actual v96/v97 native receive/583/store/UI111 branches; confirm AC and cutscene selection with explicit external fixtures, retain failed harness attempts, keep GUI/damage acceptance open.
- Verify real-PVF cross-squad delivery and actual server16 frames; add low-frequency successful-send diagnostics. Keep native animation and monster damage acceptance open; user reproduces on daily r98.
- Filter adventure-group friends to verified online characters; persist last-selected identity through the existing metadata entry and use per-account bind-generation ordering to prevent stale writes. Verify offline cold read and account isolation; native UI remains pending.
- Validate8227 core PASS/0 FAIL/736 environment SKIP and272 scoped real-PVF PASS/0 FAIL/1 separate-asset SKIP, scoped vet and Windows/Linux builds. Preserve the broader PVF run's seven assertion failures and timeout; reproduce all seven on the prior source baseline. Keep retained-QA port preflight failure explicit.
- Synchronize3383 source files to maintenance and current manifest. Production remainsr98, QA09 retained, computer control paused until2026-10-12 01:30 HK.

## 2026-10-11 — D33 raw declaration guards and CI runner diagnosis

- Reject ten unproved operation tags by raw section presence, including empty/symbol/duplicate/orphan declarations; retain atomic failure and legal floor1 controls. Independent B1 review passed.
- Final11 measured 1,058 supported/plannable level90 recipes, including273 floor recipes. Four former unresolved outputs declare empty item-category sections and now fail the raw guard; preserve1062/1058 as historical measurement.
- Validate8,220 core PASS/0 FAIL/725 environment SKIP,84 real-PVF PASS/0 FAIL/0 SKIP, go list, scoped vet and Windows/Linux builds; preserve final10 assertion failure.
- Record user acceptance of one2659502 necklace craft, retain remaining recipe/experience/cold-login work and UI pause until2026-10-12 01:30 HK. Production staysr98.
- Diagnose pipeline164381: both builds failed stuck_pending_no_matching_runners before script execution. Enable project shared runners; sole instance runner remains paused/offline. Keep CI gates and record the external runner dependency.

## 2026-10-11 — D33 bounded crafting floor and source qualifications

- Permit strict making-level1 in the eleven proved client categories; validate raw PVF shape instead of trusting the catalog projection. Preserve rejection for higher levels, skill gates, explicit rates and unknown semantics.
- Check ordinary drawing level in the owner transaction, reject restricted or malformed job fields, and conservatively reject all 39 drawings declaring unproved date semantics. Retain zero-count material audit and shared-asset rollback.
- Measure 1,062 supported / 1,058 plannable recipes at level90, including 273 floor recipes; preserve historical counts and avoid claiming native acceptance from catalog coverage.
- Validate final09: 8,097 core passes with 725 environment skips, 84 real-PVF passes without skips, scoped vet, go list, maintenance preflight and Windows/Linux builds. Keep native floor crafting and experience progression pending; production remains r98.

## 2026-10-11 — D33 native Vagabond checkpoint

- Verify one isolated level90 Vagabond support weapon through three native equip/unequip moves, unchanged item instance and durability on bag readback, and a new native client process retaining support slot24.
- Preserve combat and post-combat persistence as pending; additional item12 remains open. Shut down only the owned QA client/server and retain its database. Production remains r98.

## 2026-10-11 — D33 recipe settlement and observed native routes

- Read equipment design recipes from real PVF; fail closed for unproven rates, skill/itemmaking gates, upgrade tables and alternative source semantics.
- Settle supported drawings, character materials, account warehouse materials, gold and outputs atomically through the existing owner; reject historical character copies of account assets.
- Register observed native legacy op25 and op65 routes alongside their proved Upper owners. Refresh recipe inventory with op13 after commit; do not invent 119-byte reward fields.
- Retain rollback, locked/corrupt asset, overflow, shared refresh, cold read and native dispatch regressions; validate domain/bridge/control, real PVF, scoped vet and Windows/Linux builds (final07).
- Record isolated PID-bound native town entry and bounded recharge/diamond window recovery; verify one native 5003 craft and client cold relaunch; keep other recipes/failure paths, membership/payment, Luke and equipment acceptance open. Production remains r98; isolate the joust catalog workaround.

## 2026-10-11 — D33 candidate repairs and user acceptance

- Record user acceptance of original groups 1/2/3 and additional item 3. Production remains r98.
- Fix Vagabond support-weapon placement and integrate reviewed Luke route, prerequisite, material-drop and dual-boss candidates.
- Preserve historical source manifest; add current-source manifest and D33 validation/acceptance boundaries.
- Validate domain/bridge/control, real-PVF cases, vet and Windows/Linux builds; retain prior failure evidence.


## 2026-10-11 — Luke D33 candidate review (Luke source/test only)

- Review the Luke light/dark route owner, ordinary Mechanical Throne prerequisites, dual-boss clear gate and raid material filter against the real runtime Script.pvf.
- Confirm PHASE-2 `[LOCKOUT TYPE]` 4/5/0 = light/dark/shared: a route commits only on accepted entry, survives rejoin and group move, resets per epoch, and shared energy dungeons are never gated.
- Confirm `map/luke_normal/light_altar/55946.map` (dungeon 5104) spawns two bosses 56699/56698, so the >1 blocking-boss gate blocks the premature clear; dark altar 55953 keeps a single boss.
- Confirm the four Luke raid materials 10159142/10159143/10159623/10159649 are `[material]` and covered by the filter; light-altar essence 10159404 is `[quest]` and stays outside it.
- Document limitations without inventing rules: Mechanical Throne admission uses the persisted lifetime `DungeonPermission.ClearState` because sequence 42 and `etc/` carry no reset/day evidence; the material filter suppresses all `[material]` in the multiplayer Luke raid, which is broader than the named materials.
- Add `dungeon_luke_review_regression_test.go` and the limitation comment in `dungeon_luke_normal_rules.go`. Changed scope stays Luke source/test; no deployment, database or runtime asset change.

## 2026-10-11 — Online repository acceptance handoff

- Record source import commit 22960444f648a4f2f5bcc7b8a3fbe7086eebc5cc as merged into master.
- Add Docs/ACCEPTANCE_HANDOFF.md with previously accepted items, exact acceptance boundaries and merged source status.
- Preserve all pending GUI items and r98 deployment/paused-task boundaries; no new code or runtime changes.

## 2026-10-11 — Initial Go server source import

- Import complete 3,356-file D22/D23 source closure without changing source bytes.
- Add Windows BAT and POSIX shell build entrypoints with ignored dist outputs.
- Add Linux/Windows amd64 GitLab builds and core tests; preserve SAST include.
- Include example configuration with placeholder password; exclude runtime assets, credentials, player data and binaries.
- Record candidate/online version boundaries and pending acceptance in Docs/SOURCE_IMPORT.md.
- Build organization reference: rewio/ServerS4A21 commit 1f4a4e80610c767278008cd666588cb9a9629e34.

## 2026-10-11 — D33 latest user acceptance and volcano priority

- Close original groups5/12 on direct user confirmation; preserve groups1/2/3 and additional3. Keep original4/9 untested and original11 unreproduced.
- Prioritize Anton phase-two black-volcano barrier remaining at0%; separately track phase-one settlement return, squad board occupancy, adventure friend character selection and temporary-town/abyss admission.
- Preserve r98 deployment and UI pause until2026-10-12 01:30 HK; source tests do not prove native physical barrier removal.
