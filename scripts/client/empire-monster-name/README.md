# 困难夏洛夫名称映射补丁

仅补充原生函数 `01466150` 缺失的怪物 ID `63847`，返回与 `63843` 相同的名称索引 `66071 / 0x10217`。不改协议接收位，不读写 PVF，不新增 PE section，不部署、不访问进程。输出供主线程集成验收。

## 固定输入与证据

- 输入：`H:/codex-work/20261022/r195/resume/DNF-r195-local-face.exe`
- 输入 SHA256：`8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790`
- 大小：`111865856` 字节。
- `patch_name.py` 严格核验完整输入 SHA/大小、`1466150..14661FF` 全部字节及下一函数 `1466200` 起 15 字节；不接受 readiness-v1/v2 或任何其他摘要。
- 独立读取固定 `native_scan.py` 对应的 c7ba 客户端并反汇编本函数。其完整函数与上述输入一致，函数及 padding SHA256：`f2cef0d07555598f8bdd97df3197e421e9695ea20e6c239dc61c15445c4973cb`。
- 固定 c7ba 镜像 SHA256：`c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8`。用户提供的原版 `f781c423` 是问题背景，本轮未读取该原版；执行对照基线为完整固定 SHA 的 local-face 输入。

## 精确修改

原函数相关指令：

```asm
01466168 sub eax,0xF960         ; 63840
0146616D je  01466174
0146616F sub eax,3             ; 此时 EAX = (input - 63843) mod 2^32
01466172 jne 014661D9
01466174 mov eax,0x10217
01466179 pop ebp
0146617A ret 4
014661D9 or eax,0xFFFFFFFF
014661DC pop ebp
014661DD ret 4
```

将 `1466172` 的 `75 65` 改为 `75 7E`，跳到原本连续 14 个 INT3 的 `14661F2..14661FF`：

```asm
014661F2 cmp eax,4
014661F5 je near 01466174
014661FB jmp near 014661D9
```

空隙的新字节为 `83f8040f8479ffffffe9d9ffffff`，恰好 14 字节，末尾到 `1466200` 前结束。下一函数保持原样。

只有原 JNE 的分支目标变化。经过减法链后，32 位模运算中 `EAX == 4` 的唯一输入为 `63847`，该输入实际经过此路径。所有其他经过 cave 的输入回到原默认返回；原成功映射绕过 cave。此论证覆盖 uint32 全域；测试使用全域随机采样，**没有宣称穷举 2^32 个输入**。

## 复建和验证

使用已有轻量 Python/Unicorn 依赖；无需安装包、Go、PVF 或全镜像扫描。以下在当前目录运行，使用尚不存在产物的新外部目录（路径示例需自行选择未占用目录）：

```text
C:/Users/16ww/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/python.exe -B patch_name.py --out-dir H:/codex-work/20261022/r195/acceptance-deployment/empire-name/main-rebuild
C:/Users/16ww/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/python.exe -B verify_name.py --candidate H:/codex-work/20261022/r195/acceptance-deployment/empire-name/main-rebuild/DNF-r195-empire-name.exe --receipt H:/codex-work/20261022/r195/acceptance-deployment/empire-name/main-rebuild/verify-receipt.json
```

可通过 builder 的 `--out-dir`，以及 verifier 的 `--candidate` / `--receipt`，将独立复验产物保存至另一个外部目录。输入仍必须匹配固定完整 SHA。默认产物：

- `H:/codex-work/20261022/r195/acceptance-deployment/empire-name/DNF-r195-empire-name.exe`
- 同目录 `build-receipt.json` 和 `verify-receipt.json`。
- 首版输出 SHA256：`470dbd0bd41f0b104e1e26627852a902fcac66b6cc7755fa88f4db77e61bc34d`；该历史文件不覆盖。最终补齐 checksum 的产物见下节。

`verify_name.py` 在 Unicorn x86 中执行两份实际函数字节，无 UI/STL stub、无伪造函数返回。只映射函数页、独立栈页和返回哨兵；单次执行最多 80 条指令，越界路径无法静默通过。

实际结果：

- 全部 14 个原映射、63847 与邻值、零值/有符号边界/uint32 最大值，以及固定种子 `0x19563847` 的 4096 个全域随机样本；去重后 4189 个输入。
- 每个输入用两组初始寄存器/flags，分别运行原/改函数，共 16756 次实际执行。唯一变化为 `63847: 0xFFFFFFFF -> 0x10217`。
- 每次均检查 `ret 4` 后 ESP 增加 8，EBX/ECX/EDX/ESI/EDI/EBP 保持，栈中仅原 PUSH EBP 的槽被写入，参数及哨兵不变，代码不被写入。EAX 是返回值；不承诺新增成功路径的算术 flags 与原默认返回相同，flags 不作为函数返回契约。
- 首版全文件逐块比较确认大小不变，代码实际仅 15 个字节不同（原短跳转位移 1 字节、cave 14 字节）。最终版本额外重算 checksum；除此字段外的 PE 头、section、接收位及既有 local-face 补丁逐字节不变。verifier 只允许代码补丁和准确重算的 checksum，额外拒绝 PE 头、函数体、下一函数的单字节篡改。
- builder 和 verifier 启动前检查可用物理内存至少 3GiB，verifier 每 512 个输入再检查一次；回执包含 floor 和启动可用量。未启动其他代理或重任务。

## 收尾：拒绝覆盖与 PE checksum

builder 启动时检查输出 EXE、`build-receipt.json`、`verify-receipt.json`，任一个已存在立即失败，要求换新 `--out-dir`；没有覆盖开关。实际写出 EXE 使用 `xb`、回执使用 `x`，防止检查之后的竞争覆盖。两个文件均成功独占打开后才写数据；若竞争或写入异常，可能留下本次创建的空/部分文件，须换新目录，脚本不会删除既有文件。verifier 的 JSON 回执也使用 `x` 拒绝覆盖。

固定输入的 PE OptionalHeader.CheckSum 位于文件偏移 `0x188`，值为 **`0x06ABA175`**，独立重算与其一致。因此采用重算策略，不清零、不沿用旧值。更新后的 checksum 为 **`0x06AB8BC2`**；checksum 字段自身按零参与计算，折叠 16 位进位后加文件长度。输出仍无新增段、长度不变。全文件最终实际差异 **17 字节**，其中代码 15 字节、checksum 实际变化 2 字节（允许字段宽度为 4）。

- 最终候选：`H:/codex-work/20261022/r195/acceptance-deployment/empire-name/checksum-final/DNF-r195-empire-name.exe`
- 最终 SHA256：`9327087d147576314dd95c6c0b0a638b1525bca48734658fe4f9b97dc33f82ea`
- 同目录 `build-receipt.json`、`hardening-receipt.json` 保存构建和收尾验证。
- 新目录构建成功；EXE 单独存在、构建回执单独存在、验证回执单独存在、重复构建四种情况均拒绝，已有内容不变。
- 额外用分块逐字进位算法独立验证 checksum，并以新版 verifier 的精确差异检查确认允许范围。最终文件与已完成 16756 次原生执行的 `470d` 文件只有 checksum 字段不同，函数代码完全一致；没有重跑原生矩阵，未将收尾验证冒记为新一轮原生执行。

## 验收边界

验证证明名称索引函数的单项映射修复及精确二进制修改，不代表已完成游戏 GUI 验收。名称资源内容使用用户确认的 `63843` 同名要求；本轮没有重新解析资源文本。PVF 未被打开，接收位保持是由整个 EXE 的精确允许差异检查保证。候选包含指定 local-face 输入已有的累计内容；本轮不追加 A01/A02 readiness 实验，不改变这些历史项目的验收状态。交由主线程决定后续发布集成。

591 listener 证据已先保存至 `resume/a02-metadata-listeners/`，该调查已停止；本目录与其代码路径无关。本次未修改 changlog 或其他脚本，未提交。
