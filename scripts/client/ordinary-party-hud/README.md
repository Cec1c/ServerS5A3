# 普通队伍 HUD 局部候选 v4

本目录提供固定 v159 客户端的离线构建器与三组有界原生指令验证器。候选覆盖普通队伍 HUD 的成员点击、两种布局的 24×24 头像、横排人数显示门，以及后续状态控件使用的 Y scratch 保存。用户实机验收尚未完成；发布和玩家通道状态以独立交付回执为准。

本目录包含全部构建与验证源码。验证回执由下列命令生成，不随源码提交二进制或本机证据目录。

**固定输入与输出**

| 项目 | SHA-256 | 大小（字节） |
| --- | --- | ---: |
| v159 基础 EXE | `db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0` | 111886336 |
| v4 候选 EXE | `a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1` | 111890432 |

文件名或版本标签不能替代固定摘要。构建器要求输入大小和 SHA-256 都匹配上述 v159 基础镜像，输出也必须通过固定大小和 SHA-256 校验。输出文件名为 `DNF-hud-local-candidate.exe`，构建回执为 `build-receipt.json`。

构建器使用 Python 标准库，PE 解析、指令编码、对齐与校验和代码均内联，不依赖外部构建模块或私有机器路径。它核对 PE32、固定基址、证书表、无 overlay、新节地址和节头空间，并检查六处原始指令窗口。原文件只有六处 UI 消费者补丁和必要 PE 头字段允许变化；其余原字节逐段核对。新增 RX 节 `.hudaux` 位于 VA `0x6EC1000`，载荷含间隙为 1113 字节，文件对齐后增加 4096 字节。

构建结果先写入输出目录旁的临时目录，读回核对 EXE 摘要和 JSON 回执，再通过目录 rename 交付。该目录操作仅产生本地文件，不代表软件发布或安装。回执保留 `baseSha256`、`candidateSha256`、`size`、`helperAddress`、`helperSize`、`payloadSize`、`helpers`、`patches`、`originalByteWhitelist` 等字段，并记录窗口、PE 校验和及磁盘读回状态。

**六处 UI 消费者补丁**

以下地址均为固定镜像的虚拟地址。

| 补丁位置 | helper 入口 | 用途 |
| --- | --- | --- |
| `0x221F3B3` | `0x6EC1000` | 普通队员点击路径的成员查询 |
| `0x224A3BB` | `0x6EC1000` | 第一种 HUD 布局的成员查询 |
| `0x224AF6E` | `0x6EC1000` | 第二种 HUD 布局的成员查询 |
| `0x224A647` | `0x6EC1200` | 第一种布局的直接头像绘制及 Y scratch 保存 |
| `0x224B282` | `0x6EC1300` | 第二种布局的直接头像绘制及 Y scratch 保存 |
| `0x224A2D3` | `0x6EC1400` | 横排 HUD 的人数显示门 |

成员查询优先返回原场景槽中的 actor。场景槽为空时，仅在非 arena、已有本人 actor、本人仍属于当前 party、槽位和成员 key 有效、party 与当前连接 namespace 一致等条件下，查询精确的辅助缓存（aux）；还会核对返回 actor 的 key、owner、route 和 stats。查询 helper 不创建 actor，也不写全局场景或队伍 actor 槽。

两处绘制 helper 只在本行 actor 非空且当前场景槽仍为空时直接调用 actor 的 virtual+4。头像参数保持 `x=EDI+2`、`y=EBX+2`、`24×24`、`lazy=1`、`target=0`、`alpha=0xFF`；同场景、不同非空场景槽及空指针分支继续走原路径。人数 helper 仅在原 count 小于 2 且 flag 为 0 时扫描八个可供 UI 安全取得的成员，返回原 count 与该计数的较大值；仅重定向表中这一处人数消费者。

v4 的坐标修正位于两个绘制 helper 内：在直接绘制分支写入 `EBX+2`，第一种布局保存到 `[EBP-0x690]`，第二种保存到 `[EBP-0x688]`。旧 v3 跳过了原路径中的这次写入，后续 `0x2207430` 状态控件路径会读到旧 scratch。v4 保留该坐标供后续使用；对应验证检查状态控件位置为 `x=EDI+0x70`、`y=EBX+2`。v159 已有的头像注册 guard 被继续复用，不是新增的第七处补丁。

**命令行复现**

以下命令从本 README 所在目录执行，全部采用相对示例路径。将准确的基础镜像放在 `../inputs/DNF-v159.exe`；不要用其它 EXE 替代。使用 Python 3，保持普通模式，不能启用 `-O`、`-OO` 或设置 `PYTHONOPTIMIZE`。构建器只需标准库；三个验证器另需预先准备好 **`unicorn==2.1.4`**。验证器还会使用本目录的 `build_candidate.py`、`aux_cache.py`，其中 lifecycle 复用 `verify_draw.py` 的参数和输入校验，运行时应保留这些同目录源文件。

每次复现应选择新的输出目录；下面的 `../hud-v4-output-new` 应尚未存在。不要指向基础镜像目录或已有结果目录。已有验证回执不会被覆盖，重复验证需另选新的 `--receipt` 路径。

先执行标准库构建：

```text
python build_candidate.py --input ../inputs/DNF-v159.exe --output-dir ../hud-v4-output-new
```

随后按顺序执行三个验证命令，均使用同一份固定基础镜像、新生成的 v4 EXE 和配套构建回执：

```text
python verify_lookup.py --input ../inputs/DNF-v159.exe --candidate ../hud-v4-output-new/DNF-hud-local-candidate.exe --build-receipt ../hud-v4-output-new/build-receipt.json --receipt ../hud-v4-output-new/lookup-native-v4.json
python verify_draw.py --input ../inputs/DNF-v159.exe --candidate ../hud-v4-output-new/DNF-hud-local-candidate.exe --build-receipt ../hud-v4-output-new/build-receipt.json --receipt ../hud-v4-output-new/draw-native-v4.json
python verify_lifecycle.py --input ../inputs/DNF-v159.exe --candidate ../hud-v4-output-new/DNF-hud-local-candidate.exe --build-receipt ../hud-v4-output-new/build-receipt.json --receipt ../hud-v4-output-new/lifecycle-native-v1.json
```

三个验证器均校验固定 base/candidate SHA-256 与构建回执身份，再从镜像映射原生指令窗口，在 Unicorn 中执行有界片段。它们不启动游戏、不访问游戏进程，也不进行发布或部署。`lifecycle-native-v1.json` 中的 `v1` 是该验证回执的命名，不是客户端候选版本；当前回执对应上表的 v4 摘要。

**已有验证证据**

以下三组回执均为 `passed: true`，候选摘要均为上表 v4。695、48、64 是不同验证组的用例数，分别陈述，不合并为完整 GUI 或生命周期验收数量。

| 验证组 | 当前结果 | 覆盖范围 | 既有回执 |
| --- | --- | --- | --- |
| Lookup | 695 项通过 | 三个成员查询调用点、八槽及非法槽、namespace/key/owner/route/stats 拒绝条件、场景命中优先、点击片段、第一布局可见性与 stats 门、横排人数门；检查对象和缓存不被改写 | `lookup-native-v4.json` |
| Draw | 48 项通过 | 两种布局 × 四种 actor/场景分支 × 三组坐标 × 有无现有模型；检查 24×24 参数、寄存器/flags/栈、Y scratch，以及 aux 分支后续状态控件坐标 | `draw-native-v4.json` |
| Lifecycle | 64 项通过 | 两种布局 × 五个布尔条件的组合：现有模型、descriptor、UI 注册、remote namespace、其它活动 actor；覆盖创建或复用、重复绘制、reset、再次 reset 无动作、重新创建和再次 reset | `lifecycle-native-v1.json` |

`build-receipt.json` 记录固定输入输出、六处补丁、完整原字节白名单、四个 helper、PE checksum 和磁盘读回通过。

`old-v3-coordinate-red.json` 保留两项旧 v3 红测：在 `0x224A647` 和 `0x224B282`，旧候选 `375d6fa72c6a8e06ebc05ede20473d4c9e688e64a0ef9899770621b58d9bf716` 均预期失败于 `status-coordinate local`。该回执的 `passed: true` 表示成功观察到预期失败，不表示旧 v3 坐标正确。这两项是独立的缺陷对照，不计入上述三组通过数。

**原生执行与 fixture 边界**

Lookup 组执行真实队伍槽 getter、成员 key 扫描、namespace getter、八个 aux 缓存函数及点击/显示门片段。namespace 锁和受保护值解码、arena 查询、点击命中测试、点击门中的本人 actor getter、菜单打开末端，以及缓存/party/stats 对象由 fixture 提供。点击片段通过不等于实际菜单窗口已打开或可交互。

Draw 组执行两个候选 helper、原队伍槽 getter 和 `0x29674E0` 头像分派；在 aux 分支后另从后续调用点进入 `0x2207430`，执行到 `0x37EA170` 定位调用边界。中间 HUD 装饰代码未执行，状态判断与定位末端、模型构造、虚拟渲染末端和 UI frame 仍是 fixture。该组没有在同一次执行中覆盖真实 factory 和 v159 注册 guard。

Lifecycle 组在同一有界流程内执行候选绘制 helper、原生头像分派、**模型 factory `0x2965BD0`、actor 模型 writer `0x29658D0`、reset `0x2965750` 和 v159 注册 guard `0x6EC0200`**，检查模型复用、reset 清空 `actor+0x4F4`、重复 reset 无动作及重新绑定模型，同时检查场景/队伍表和 stats/descriptor 保持。

该流程中的 **allocator、资源构造和外观加载、virtual render、destructor 末端仍是 fixture**；受保护身份解码、模型元数据身份写入、模型初始化子调用（269E170/26A1440/269E700/26CFB40/352F8B0）、namespace 判断、活动 actor 查询以及 UI/description 注册表回调也由 fixture 提供。原生 factory/writer/reset/guard 的执行证明的是这些固定指令窗口在所列边界下的行为，不能据此宣称真实时装资源已加载、实际分配器和析构已完成验证、GPU 已绘制，或完整模型生命周期与 GUI 已验收。

当前结果仅支持固定镜像的可重复构建、所列 UI 消费者片段和有界模型流程。完整 HUD、真实资源加载、用户交互及游戏运行中的生命周期仍未验收；不能据离线检查标记用户实机验收通过。

也可使用统一入口串行执行上述三组验证，三个组均通过后才生成 summary.json：

```text
python verify_candidate.py --input ../inputs/DNF-v159.exe --candidate ../hud-v4-output-new/DNF-hud-local-candidate.exe --build-receipt ../hud-v4-output-new/build-receipt.json --output-dir ../hud-v4-validation-new
```

统一入口固定 unicorn==2.1.4，并拒绝任何已有输出目录。它保存每组日志、回执和源码摘要；失败保留诊断，不写总通过汇总。

生命周期组映射完整41字节注册guard；64组合覆盖辅助对象不进入场景注册的路径。允许注册的活动对象分支不在该组覆盖范围内。
