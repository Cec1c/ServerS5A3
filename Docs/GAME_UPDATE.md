# 游戏服务更新入口

后续 agent 收到已授权的游戏服务更新任务时，直接使用根目录 `GAME_UPDATE_REMOTE.bat`。用户不需要手动执行。Windows 控制器为 `deploy/windows/game_update.py`，Linux worker 为 `deploy/remote/game_update.py`，每次经 SSH 标准输入执行并保存版本回执，不另写临时替换脚本。

这条流程更新已配置的远端 Docker 实例的游戏二进制，保留 GM、数据库、玩家数据、PVF、频道与登录器。需要更新客户端或内容时，先完成 [CLIENT_RELEASE_SYNC.md](CLIENT_RELEASE_SYNC.md) 的 Release、实际玩家通道和摘要核验，再执行依赖它们的游戏更新。Git 提交、推送及 PR/MR 仍按 [REPOSITORY_SYNC.md](REPOSITORY_SYNC.md) 单独处理。

客户端前置要求见 [anti-RE 加锁与最新发行来源](CLIENT_RELEASE_SYNC.md#anti-re-加锁与最新发行来源)：先验证最新适用 Release 的 EXE 已按 anti-RE CLI 契约加锁，再将其最终摘要与实际玩家签名清单、下载文件和部署 profile 绑定。现有签名及配置摘要一致检查不能单独证明加锁状态或发行的新旧；维护此入口时必须核对发布链已接入这些检查，缺项先补齐再进行新发布或依赖部署。

## 本机配置

私有目标配置在被忽略的 `runtime/game-update/profile.json`，格式见 `deploy/templates/game-update.example.json`。优先读取已有配置和 `local-notes/AGENT_HANDOFF.md`，不要覆盖已有环境信息或猜测地址。配置引用 SSH alias、玩家签名 profile 和无 GUI 探针，不写密码、私钥或 token。

依赖 Python 3.10+、`cryptography`、Git、OpenSSH 和源码要求的 Go 工具链。远端要求 Python 3.11+、Docker Compose、匹配实例的现有部署生成器和备份工具，以及受控的无交互 sudo。SSH 强制核对已知主机密钥。`probe_command` 指向已批准登录器的无界面频道探针；登录器和 GM 产物由各自仓库维护。本入口不初始化实例，也不自动替换远端生成器；其窄配置映射须在原部署环境完成验收。

`runtime_base_image` 必须是已验收的完整 `sha256:` 镜像 ID。后续构建始终替换该固定运行基础镜像中的服务二进制，避免每次在上一次镜像上继续叠加层。worker 检查镜像的入口、环境、用户、端口及健康配置与当前实例一致；基础环境变化应单独审查。

## Agent 执行顺序

**每次新部署强制当场拉取权威主干。** 控制器在本次 run 的独立干净目录执行 `git pull --ff-only --no-rebase https://gitgud.io/16ww/servers5a3.git master`，核对远端 master 读回；常规部署要求该提交是候选的祖先，不读取历史回执授权部署。共享脏工作树保持原状。Windows 的独立拉取目录设置仓库级 `core.longpaths=true`，不修改全局 Git 配置。

首次验证前、打包前、上传前及远端 prepare/generate/deploy 各阶段都重新 pull。每次保存来源、分支、执行时间、拉取前后 SHA、候选 SHA 与本次随机运行标识。拉取失败、分叉、缺少主干或验证期间主干变化，均停止当前 run；集成并重新验证后使用新 run ID。源码 manifest 的 upstream 来自本次成功拉取，不再继承旧部署清单。

远端变更阶段核对本次 proof 的摘要、运行 ID、阶段、候选、主干和随机标识，超过五分钟或与构建清单不同就拒绝。inspect、verify、rollback 不要求拉取，以便断网核验及恢复旧版本。

1. `GAME_UPDATE_REMOTE.bat inspect`：读取实际进程、镜像、累计源码清单、兼容资产和在线连接数。以远端 `runtime/active-game-baseline.json` 为部署基线，不以上游或本地 master 代替。
2. 在干净的隔离工作树中基于该累计提交合入最新权威主干和精确修复，保留已上线的私有扩展。当前累计源码仓库位置从私有交接索引读取；需要时从该本地仓库获取对象，不向公共仓库推送私有基线。候选必须是当前累计提交及最新主干的后继，逐项审查有效改动，不能用仅保留一方的形式合并充当集成。
3. 审查完整差异，将本次全部变更路径写为私有 JSON 字符串数组，例如 `runtime/game-update/reviewed-paths.json`。脚本严格比对，不自动接受整棵脏工作树。`go-server/` 及附带文档按已审范围打包；其他上游路径只有内容与本次权威主干完全相同才可随源码归档，不据此安装部署工具、发布客户端或替换运行配置。这些运行变更仍使用各自的已授权流程。
4. 执行 `GAME_UPDATE_REMOTE.bat deploy --source <干净累计源码目录> --changed-files <已审查路径JSON>`。它固定候选提交和文件摘要，运行 list、领域/bridge/control 测试、vet、Windows 构建及 Linux 构建，然后打包、上传、生成配置、备份和切换游戏服务。测试日志持续写入回执目录，运行期间向用户提供进展。
5. 脚本等候健康检查，再核对 `/proc/<pid>/exe`、镜像、环境与 UID、GM/DB 容器身份、PVF、频道、签名玩家清单及频道探针。成功后执行备份保留；清理失败记为待执行，不撤销已验证的游戏部署。
6. 更新私有交接和 BUG 状态；自动化验证与游戏内验收分别记录。脚本通过不代表玩家交互已验收。

候选与线上相同且工作树干净时，`deploy` 执行签名、运行状态和频道检查，写入 `no-op.json`，不构建、不备份、不重启。仍须传空路径数组 `[]`，不能用相同提交绕过脏工作树校验。每次运行使用独立 run ID，已有部署回执不可覆盖。

### 原累计 Git 对象丢失时

先取得 active 指向的完整源码归档与 manifest，验证归档总摘要和每件文件。将原字节导入一个明确标记的**新根提交**，没有伪造父提交，也不声称恢复了旧 SHA。随后正常集成最新 master 与修复，保留双方有效逻辑，并保存累计仓库或 Git bundle。

使用 `deploy --recovered-base <新根提交完整SHA> ...` 时，入口读取该提交的所有 Git blob，要求路径集合及逐件 SHA256 与本次在线 snapshot 完全一致，且候选包含此新根提交。带父提交、缺失或多余文件、换行变化或其他字节差异都会拒绝。常规祖先检查、最新主干、完整范围审查和全部验证继续执行。

回执记录原 active sourceCommit、归档摘要、新根提交、逐件核验数量与 `gitAncestryRecovered=false`。新 manifest 的 baseCommit 仍指向实际旧 active，新增 sourceRecovery 说明来源；不预先改写线上身份来绕过部署检查。

每次新包同时保存 `game-source.bundle`，本地先执行 `git bundle verify`，远端按 manifest 的 `sourceGitBundleSha256` 校验后保留在同一 run。后续可从 bundle 恢复实际累计提交与其完整祖先，不依赖某个临时工作树继续存在。bundle 含私有累计源码，只作为私有部署证据保存。

### 明确授权的紧急定点恢复

用户明确要求先恢复服务、其余上游更新后续集成时，仍执行本次各阶段的真实 pull。当前 `--emergency-evidence` 仅支持已合并 MR !226 的六文件恢复：绑定原 active、源码归档摘要、新恢复根、候选提交、六文件前后摘要及全部源码字节，并核对选取的合并提交确实位于当场拉取的 master 中。普通部署的完整主干检查保持。

复用验证必须同时核对原验证目录的全部源码、完整验证结果和日志摘要。Linux 发布产物另验完整摘要、平台及 `timetzdata`；缺少时只补必要发布构建。此模式禁止改动运行配置，备份、回退、实际程序和玩家签名／频道检查全部保留。

回执明确记录 `containsMaster=false`、选取 MR 与当场观测的 `observedUpstream`；`upstream` 保留原累计清单的来源记录，不把观察到的新主干冒充已完整集成。失败 run 保留，切换中断先读回实际 active，再决定下一步。分机部署备份游戏机实际配置、数据库和旧镜像，同时留存已验证的网关快照，不要求游戏机存在网关的本地 state 文件。

大会模式可随已审查的源码更新一同变更：私有更新 profile 显式设置 `dungeon_tournament` 为 `off` 或 `vendor`，省略时保持实例现值。该值固定进本次 manifest，由 worker 更新实例源配置的同名字段并调用生成器，映射到 `DNFBRIDGE_DUNGEON_TOURNAMENT`。不接受其他值或任意环境变量覆盖；相同源码但请求不同配置会拒绝，不能把配置更新误报为 no-op 成功。启用前须核对匹配客户端、PVF 及玩法支持边界。

主线任务经验倍率使用独立窄配置项 `main_quest_exp_multiplier`，只接受整数 `1..100`；省略时保持实例现值，未配置时服务端默认 `1`。该值冻结为 manifest 的 `mainQuestExpMultiplier`，由实例生成器映射到 `DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER`。控制器、worker 和生成器都拒绝布尔、字符串、浮点与越界值；相同源码但源配置值不同会拒绝，须随经过审查的源码更新执行。不要传通用环境变量覆盖。

倍率更新只允许实例的这一字段、对应环境键和游戏镜像变化；大会模式、GM 拓扑、数据库和其他环境保持。`inspect` / `verify` 读回源配置、容器及 `/proc` 的实际值，部署后必须一致；环境文件沿用失败回退与手动回退，保留当前玩家数据库。控制器和远端实例生成器应先完成独立运维维护，再部署依赖倍率的游戏源码，不能把工具文件混入游戏 changed-files。

生成器必须具有此映射；缺失时明确失败，不直接编辑生成的 env。切换只允许镜像和显式指定的大会／主线经验变量变化，其余环境逐字节/逐键保持；实际进程与容器的大会模式、客户端 profile 共同读回。环境文件与实例、Compose、active 一同备份及恢复，自动失败回退和手动回退均保留当前玩家数据库。更新生成器/控制器属于独立运维工具维护，不混入游戏源码 changed-files。

默认全量 Go 测试不设置真实 PVF 或玩家测试 DSN。需要真实 PVF 专项时，在私有 profile 的 `real_pvf_commands` 配置已审查的 argv 数组；脚本先验证 `pvf_file` 与运行 PVF 摘要相同，再只对这些命令设置 `DNFBRIDGE_REAL_PVF_SMOKE`。数据库专项另用一次性隔离数据库，不能向脚本传入真实玩家库。

## 游戏与网关分机部署

迁移后游戏机与账号／签名／外部 GM 网关可使用不同 SSH 目标。私有 profile 可设置 `edge_ssh_alias` 和 `edge_remote_root`，仍由 `GAME_UPDATE_REMOTE.bat` 统一调用。每个阶段先通过已固定主机密钥的 SSH 读取网关实际进程、PVF、六类索引、私有接口与签名发布状态，再将带 SHA256 和采集时间的证据传给游戏 worker；超过五分钟、传输摘要错误或玩家／GM PVF 不同均拒绝。

游戏机直接核验实际累计源码、进程、镜像、实例配置与频道文件；网关配置和索引摘要作为独立证据保留，不复制一套假的 Hub/GM 运行目录。新的实例可以直接使用生成器 `manifest.json` 和配置指定的 `channel.override.etc`。旧的单机 profile 沿用原路径。

迁移初始化仍先完成原数据库归档、空库与资源发布，再建立实际 active 和新 profile。旧 SSH 别名对应的 profile 应归档；新目标通过 inspect、verify 和相同源码的无重启检查后再恢复默认入口。

## 回退和中断恢复

每次切换前保存数据库 dump、配置、旧镜像归档、源码清单和旧 active 指针。默认只改 `instance.json` 的 `server_image`；显式大会模式或主线经验倍率另外允许对应窄配置项。调用现有配置生成器，要求 Compose 差异只有游戏镜像，保持部署用户及权限。

切换后的运行检查失败会恢复旧游戏镜像、配置及 active 指针；后续频道探针失败也会调用回退。数据库保持当前玩家进度。手动回退使用 `GAME_UPDATE_REMOTE.bat rollback --run-id <原部署ID>`，它拒绝覆盖另一项已上线部署，并再次核对进程与频道。

SSH 在切换期间断开时，控制器先重新查询结果，不重放部署命令。如果仍无法确认，报告“部署结果未知”，保留原 run ID；恢复连接后先 `verify` 并查看该次远端回执，不能直接新建一次部署覆盖现场。服务器备份目录还保存独立 `rollback-game.py`，仅供恢复控制器无法使用的情况；执行后必须做 `verify`。

回执在本地和远端的 `runtime/game-updates/<run-id>/`，包括候选范围、测试日志、源码与二进制摘要、兼容核验、备份、部署、回退及频道结果。这些目录属于私有运维资料，不加入 Git。`inspect`、`verify` 不切换服务。

## 备份保留

`GAME_UPDATE_REMOTE.bat cleanup` 只输出计划；`cleanup --apply` 应用。成功部署后自动应用，远端每日定时器也会执行，无需用户手动清理。

- 始终保留当前及最近两个不同成功部署的配置/镜像和关联数据库回退点。
- 数据库保留最新 3 份，近 7 天每日 1 份，近 4 周每周 1 份；部署引用的备份额外保护。
- 新流程的 `game-update-<UTC时间>` 快照至少保留最新 3 份和仍被引用的快照。
- 未纳管的旧目录只删除内容摘要完全相同的重复副本，不仅凭年龄删除未知恢复资料。

清理与部署共用锁；删除前重验 active 指针、路径、修改时间及内容摘要，并完整解压校验保留的数据库备份。拒绝符号链接、越界路径和已变化的备份。清理回执保存在 `runtime/backup-retention/`。不删除数据库卷、源码证据、签名密钥或运行配置，也不执行全局 Docker prune。

新环境需安装 `deploy/remote/backup.py`、`backup_retention.py` 和 `install_backup_retention.py`，保留部署用户和权限，然后由 agent 执行：

```text
sudo python3 <项目根>/deploy/remote/install_backup_retention.py --root <项目根>
```

独立 root 清理 timer 每日 20:30 UTC 执行，保留既有数据库备份 timer。当前环境是否已安装以私有交接和 `systemctl` 读回为准。

## 脚本验证

```text
python -m unittest discover -s deploy/windows -p "test_game_update*.py" -v
python -m unittest discover -s deploy/windows -p test_authoritative_source.py -v
python -m unittest discover -s deploy/remote -p "test_game_update*.py" -v
python -m unittest discover -s deploy/remote -p "test_backup*.py" -v
GAME_UPDATE_REMOTE.bat inspect
GAME_UPDATE_REMOTE.bat verify
```

流程测试使用临时仓库，覆盖候选范围、基线分叉、变更期间移动提交、源码归档、相同版本、断线结果恢复、探针回退和清理失败；不会修改真实玩家数据库。仅改这些 Python 工具时运行相应测试，无须重新运行全部玩法用例。
