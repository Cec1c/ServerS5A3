# 攻坚队本人静态头像：空本人缓存候选

固定 v160 中，成员头像回退在本人 `scene+0x70` 缓存尚未填入时直接返回原路径，即使 canonical 本人查询能够取得当前行 actor、辅助头像资料也有效。本候选把这一次直接字段读取换成原生 `296DD80(scene)` 查询，随后继续原有非空和指针相等校验。只有 getter 返回当前行本人，且原有 raid namespace、aux key 和 descriptor 条件全部通过，才绘制静态头像。

这是对一个已由合成输入复现的缺口的局部修复，不代表现场黑框根因全部确认。真实缓存内容、资源/GPU和用户实机验收仍未覆盖；发布状态以单独交付回执为准。

| 文件 | SHA-256 | 字节数 |
| --- | --- | ---: |
| 输入：已发布 v160 | `a5e4e16e63d0c3bb6d085275a1c43abd62b37c4e15b4367388c6e4bde3a90cf1` | 111890432 |
| 输出：校验和修正后的候选 v2 | `5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc` | 111890432 |

## 行为与改动范围

原 `0x6EAE03A` 的 `mov eax,[ecx+0x70]` 三字节替换为五字节 `call 0x296DD80`；其后的内部跳转和外部调用重定位。`0x6EAE000` 的 helper 从249变为251字节，仍处于原 `.locface` 的4096字节页内。入口 `0x110B16A`、文件大小、节数量和其余功能保持。

构建器只允许 helper、该节 VirtualSize 和 PE CheckSum 三个窗口改变，共187个实际差异字节。独立原生指令比较的76条指令中，语义改变只有上述一次查询。构建器验证原校验和，重算并核对新校验和 `111922510`，核对新节字段和固定输出摘要，再写入新目录并从磁盘读回摘要。

canonical getter 在空缓存时解码本人 key，调用 active actor 查询，并把结果写入 `scene+0x70`。因此本候选不是完全只读：返回另一 actor 时仍保留 canonical 缓存写入，但拒绝为当前行绘制；返回零时保持零，后续会再次查询；非零缓存后续直接复用。该 getter 不在所执行窗口内创建模型或注册场景对象。

头像仍为原有静态 `14×14` 绘图，参数是控件 `x-17`、`y-2`，image 参数为 `aux+0x34`，target=0、alpha=255。`aux+0x30 != 0` 只是已有字段门槛，不能当作真实图像资源已加载的证据。辅助资料缺失、key不符、无当前本人或其它门槛失败时，继续走原路径。本候选不补发 self mode8，不修改服务器或奖励名单。

## 复现

Python 3 构建只使用标准库；验证额外依赖 `unicorn==2.1.4`。不要启用 `-O`/`-OO` 或 `PYTHONOPTIMIZE`。以下从本目录执行；输入必须是表中准确镜像，输出目录与验证回执必须尚不存在。

```text
python build_candidate.py --input ../inputs/DNF-v160.exe --output ../self-cache-output-new
python verify_candidate.py --input ../inputs/DNF-v160.exe --candidate ../self-cache-output-new/DNF-a03-self-cache-candidate.exe --build-receipt ../self-cache-output-new/build-receipt.json --receipt ../self-cache-output-new/native-result.json
```

构建器不安装、运行或发布客户端；已有输出、错误基线、优化模式均拒绝。验证器只读固定镜像，通过 section table 映射窗口，不连接游戏进程或采样其内存。

## 验证范围

50个case、62次调用：候选48项/58次，旧版对照2项/4次。保留38项门槛、坐标和flags覆盖，另覆盖空缓存取得本人/零/其他对象及两次连续调用。旧版同输入反例必须不绘图，候选取得本人后必须绘图；非本人不得绕过原条件。

真实执行入口跳转、251字节helper、`296DD80`主体及两处完整跳表（共0xD0字节）、嵌入descriptor地址getter与两个坐标getter。固定受保护地址低位8实际选择 `37DF630`；五类decoder入口均设置边界，但不宣称五个分支都执行。

受保护值解码、active查询、raid/aux查询、绘图末端及对象/调用者frame为明确夹具。检查寄存器与flags、绘图七参数、调用次序、继续地址、栈/数据保持，栈外只允许真实 `296DE1F` 写入预期的 `scene+0x70`。在 `110B16F` 或 `110B199` 继续地址执行前停止，不执行完整页面、真实模型生命周期或GPU。部分缓存成功写入后，后续aux失败也不回滚canonical缓存；当前连续序列主要覆盖上述三种本人查询结果。

回执包含源码摘要、输入/原生窗口摘要、具体用例与夹具边界。离线通过不能替代用户实机验收，也不能据此关闭其余 A01/A02/A03 条目。
