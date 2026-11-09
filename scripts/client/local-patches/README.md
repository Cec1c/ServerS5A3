# 我方本地客户端补丁栈

这里收录**独立于上游**、由我方维护的 DNF.exe 补丁。上游发布新 EXE 后，用这里的脚本把我方补丁重新叠加上去，不靠手工比对。上游不会改动这个目录，合并上游源码时不会冲突。

| 文件 | 作用 |
| --- | --- |
| `stack.json` | 补丁清单（按顺序应用）和版本登记（每个已生成版本的上游输入、包含的补丁、输出摘要、发布状态） |
| `patches/<补丁>.py` | 一个补丁一个模块：背景说明、按特征定位、判断状态、应用 |
| `verify/verify_<补丁>.py` | 在模拟器里执行改动前后的真实代码做比对（依赖 `verify/requirements.txt`） |
| `apply_stack.py` | 统一入口：检查或生成 |
| `pe_image.py` | 共用 PE 工具，只用标准库 |
| `UPSTREAM.md` | 上游 EXE 发布页地址和每一版的内容、服务端配套开关、我方派生情况 |
| `check_upstream.py` | 列出上游新版本；`--download` 下载并核对摘要 |

## 当前补丁

| id | 内容 | 起始版本 |
| --- | --- | --- |
| `pvp-relay-gate` | 擂台赛接收门放行 class-0 112/113，见 `Docs/PVP_RELAY_DICE_20261102.md` | v2.0.8 |
| `imeup-all-keys` | 松键复核从方向键扩大到全部键盘键，修 WASD/技能键卡住，见模块内说明 | v2.0.8.2 |

## 上游出了新 EXE 怎么合并

0. `python -B check_upstream.py` 看有没有新版本，在 `UPSTREAM.md` 记一行，再用 `--download <tag> --out <新目录>` 下载。
1. 先检查，不生成文件：

   ```text
   python -B apply_stack.py --input 上游新版.exe --check
   ```

   每个补丁报告一种状态：
   - `absent`：特征完整，可以自动套上。
   - `applied`：输入里已经有了，比如上游自己修了同样的问题，或者输入本身就是我方版本。
   - `incompatible`：上游改了相关代码，特征对不上，**必须人工移植**。按模块说明重新定位，更新模块里的特征或地址后再跑。

2. 全部不是 `incompatible` 后生成：

   ```text
   python -B apply_stack.py --input 上游新版.exe --output 某目录/DNF.exe
   ```

   脚本只允许补丁声明过的字节和 PE 校验和发生变化，任何其他差异都会直接报错。

3. 对每个补丁跑验证（需要 `pip install -r verify/requirements.txt`）：

   ```text
   python -B verify/verify_pvp_relay_gate.py  --base 上游新版.exe --candidate 某目录/DNF.exe
   python -B verify/verify_imeup_all_keys.py  --base <只差这个补丁的版本> --candidate 某目录/DNF.exe
   ```

4. 在 `stack.json` 的 `releases` 登记新版本：版本号、上游版本和 SHA256、包含的补丁、输出 SHA256、发布状态。版本号规则：在上游版本上派生的，在上游号后面加小版本号，如 V208 → v2.0.8.1、v2.0.8.2，V211 → v2.1.1.1。
5. 把生成的 DNF.exe 交给负责人，在网关管理页上传发布。EXE 本身不进仓库。

## 新增一个本地补丁

- 在 `patches/` 新建模块，提供 `ID`、`SUMMARY`、`inspect(img)` 和 `apply(data)`：
  - `inspect(img)` 返回 `(status, info)`，`status` 取 `absent`、`applied` 或 `incompatible`，判断不了就报 `incompatible`。
  - `apply(data)` 只能在 bytearray 上改自己声明的字节，并返回改动区间。
- **按特征定位，不认整个文件的 SHA**。用字节特征或调用关系找补丁位置，并校验周围的分支和挂接是否对得上。这样上游小改时能自动套上，大改时会明确报错。
- 模块开头写清楚现象、根因、改法、刻意不做的事，以及定位依据。
- 配一个 `verify/verify_<补丁>.py`，比对改动前后真实代码的行为。
- 在 `stack.json` 的 `patches` 末尾登记。顺序就是应用顺序，后加的补丁不要依赖改动前面补丁的字节。

## 边界

- 脚本只读输入、写新文件（输出已存在时拒绝覆盖），不运行游戏，不发布。
- 模拟验证只覆盖改动涉及的代码，不能代替实机验收。
