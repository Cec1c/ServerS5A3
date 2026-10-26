# 小队奖励卡片当前成员门禁：固定 v161 构建器

`build_candidate.py` 从固定 v161 直接生成三入口候选，只依赖 Python 3.10+ 标准库，内嵌 PE32 解析、Code 编码、对齐与 PE checksum。输入路径由 CLI 提供。

候选已独立复建并读回完整摘要；配套验证器覆盖 620 组有界原生检查，范围见 [验证说明](README-verifier.md)。发布、玩家通道和实机验收状态分别记录，构建器本身不执行安装或部署。

| 项目 | 固定 SHA-256 | 文件大小 |
| --- | --- | ---: |
| 输入 v161 EXE | `5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc` | 111890432 |
| 目标 row-gate v2 EXE | `04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad` | 111894528 |

从本目录调用，输入文件可以放在任意位置，路径只由 CLI 提供：

```text
python build_candidate.py --client-exe ../inputs/DNF-v161.exe --output-dir ../outputs/a02-row-v2-new
```

`../outputs` 必须已经存在，`a02-row-v2-new` 必须尚不存在；即使已有输出目录为空也会拒绝。不能使用 Python `-O/-OO`。输出目录包含：

- `DNF-row-gate-experiment.exe`
- `build-receipt.json`

程序先检查固定输入大小与摘要并完成内存构建；全部字节约束通过后，使用排他 `mkdir` 取得新输出目录，再以 `xb`／`x` 排他创建两个文件。EXE 磁盘读回的大小和摘要匹配后才写回执，回执也会读回比较。I/O 中断可能留下本次新建的部分输出目录，程序保留它供诊断，后续运行应换新目录，不会删除或覆盖已有内容。

## 最终布局与原字节白名单

`.rrow` 从 VA `0x6EC2000` 开始，权限 `0x60000020`（RX code），VirtualSize 为 `0x39A`／922 字节，含 helper 之间的零间隙；RawSize 为 4096 字节，其余尾部保持零。输入到输出只增加这一个 section。

| 外层 row vtable 项 | 原目标 | 新目标 | predicate 拒绝时的返回值 |
| --- | --- | --- | ---: |
| `0x491A078`（`+1C`） | `0x3829190` | `0x6EC2300` | 1 |
| `0x491A0C4`（`+68`） | `0x3826930` | `0x6EC2340` | 0 |
| `0x491A074`（`+18`） | `0x3829F50` | `0x6EC2380` | 1 |

predicate 位于 `0x6EC2000`，长度 339 字节；三个 wrapper 各 26 字节。wrapper 允许分支恢复 ECX 后尾跳原目标；拒绝分支按表返回。四段代码由原编码算法生成，同时逐字节比较内嵌的既有 v2 helper 期望值。新增 PASS wrapper 直接放入最终布局，无中间 v1 EXE。

原文件范围内只允许八个半开区间改变，已在代码内固定，计算出的范围必须完全一致：

| 文件偏移 `[start,end)` | 用途 |
| --- | --- |
| `[310,312)` | NumberOfSections |
| `[332,336)` | SizeOfCode |
| `[384,388)` | SizeOfImage |
| `[392,396)` | CheckSum |
| `[2272,2312)` | 新 `.rrow` section header，原 40 字节必须全零 |
| `[72458356,72458360)` | row vtable `+18` |
| `[72458360,72458364)` | row vtable `+1C` |
| `[72458436,72458440)` | row vtable `+68` |

追加范围为 `[111890432,111894528)`。构建器核对三项原 vtable DWORD、section header 空间、固定基址、证书目录、无 overlay 及新节地址；原文件白名单之外逐段比对。最后验证四个 helper、全部 vtable 指针、零间隙／填充、PE checksum、固定输出大小与完整输出 SHA。任一项不符都不会写出正常回执。

## 原算法与 refcount 写入

保留的行为是：对 category0、非 arena 且能取得有效缓存本人身份的行，保留本人；无 party 时拒绝其他有效 owner；party 中本人关联与 namespace 一致时按完整 key 表判断该 owner。无效 owner、未知／不匹配上下文沿原 allow 分支。没有引入当前 party 之外的新名单来源，没有新增行重排、visibility 写入或持久隐藏缓存。

**predicate 的父引用取得和释放有 refcount 写入。** 它经原 weak-to-shared getter 暂时取得父 strong reference，随后按原 shared release 序列递减计数；一般保留对象的计数恢复，归零分支可能调用 disposal。不能写成“gate 无运行时状态写入”。它不直接写 party keys、reward records、visibility、位置或 hidden-state cache；零计数 disposal 的完整副作用不由本构建器验证。

## 回执与验证器接口

模块保留 `PE`、`Code`、`align`、`u32`、`pe_checksum`、`VA/DRAW/INPUT/PASS`、`predicate()`、`wrapper()` 和 `build(data)`。`build(data)` 返回最终 v2 的 `(bytes, receipt)`，不做文件 I/O，不运行 native；CLI 另负责排他输出和磁盘读回。`BASE_SHA` 与 `BASE_SHA256` 都表示固定 v161 输入摘要。

回执保留 `baseSha256/candidateSha256/size`、`helpers`（含 `address/size/code`）、`patches`（含 `site/original/target`）、`originalByteWhitelist`、`version=2`、`directInputPassGuard` 及实验／部署／验收字段；新增 helper 摘要、原字节窗口、完整新节信息、checksum、直接构建标记和磁盘读回标记。

`v1Sha256`、`v1ToV2ByteWhitelist` 属于旧串接实验的中间产物证明，本直接构建器不生成这两个字段。验证器应以固定 v161→最终 v2 的摘要、三个补丁与原字节白名单为输入合同，不依赖已经构建过 v1 的假设。

本构建器的成功仅表示固定文件生成和字节核对通过。保留 `experimental=true`、`deployed=false`、`guiAccepted=false`；不触发提交、发布、客户端安装或服务器操作。
