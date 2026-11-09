# 攻坚队信息窗口即时刷新候选

固定输入与输出见构造回执。配方只接受现行累计输入的完整摘要，核对名单通知 mode3 的原调用与 RX 预留区；其他输入拒绝，不关闭版本校验。

常规名单通知先更新成员模型并调用原有管理器，但已经打开的详情窗口还可能保留旧控件。查询成功路径会显式刷新该窗口。候选在 mode3 原调用返回后复用这一步，只接受原生类型检查通过、窗口 raid key 与刚解析的名单 raid key 相同的现有窗口。其他窗口、旧目标及关闭状态都不创建或切换窗口。

构造与验证均不安装、不执行游戏、不读取进程。先在独立目录构造：

```text
python scripts/client/raid-live-roster/build_candidate.py --input <配对原件> --output-dir <新候选目录>
python scripts/client/raid-live-roster/verify_native.py --input <配对原件> --candidate <候选目录>/DNF.exe --report <新的回执路径>
```

验证依赖 `unicorn`，构造只需 Python 标准库。检查实际调用点和 helper 的 x86 指令、原管理器调用一次、窗口存在/类型/raid key、原调用的结果寄存器和标志，以及栈和原通知数据保持。管理器及 UI 叶函数使用夹具，不执行渲染器；实际常开窗口、多人编队、入团退团及战斗状态仍须分别游戏验收。

候选必须与对应服务端修复一起核对累计输入后再安排配对更新。源码、独立候选、安装、正式发布与游戏验收分别记录。

`movie_prompt_guard.py` 是上述固定候选后的局部增量。失败电影后续输入可以重试 NOTI599，但原生提示函数会关闭并重建已有的确认框；该增量只在这一个网络通知调用点先检查确认框 0x332，已存在便保留原窗，无确认框时仍走原提示函数。它不改变电影状态机、取消/确认回调或其他调用者。

使用 `movie_prompt_guard.py --input <上述固定候选>/DNF.exe --output-dir <新目录>` 构造，随后用 `verify_movie_prompt_guard.py --input <上述固定候选>/DNF.exe --candidate <新目录>/DNF.exe --report <新JSON>` 核对。六项有界对照覆盖管理器/窗口状态与原生 ABI；窗口与电影叶函数为夹具。它只保护重复提示的窗口寿命，失败首次提示就绪与完整跳过仍须实机验证。

`failure_movie_close.py` 继续接受上述提示保护后的固定累计输入。原生确认回调发送完成并清理输入标记，却未关闭仍在播放的失败电影；本增量仅在确认当前安徒恩 `1-F.avi` 或 `2-F.avi` 时，执行原回调后调用原生电影状态 3 关闭现有窗口。取消、成功电影、其他内容和已替换的电影对象沿用原回调。

使用 `failure_movie_close.py --input <提示保护候选>/DNF.exe --output-dir <新目录>`，随后运行 `verify_failure_movie_close.py --input <同一输入>/DNF.exe --candidate <新目录>/DNF.exe --report <新JSON>`。有界 x86 对照执行真实原回调和原生状态分发器，验证完成包只发一次、两阶段失败才关闭、栈/寄存器保持。窗口和报文写入叶函数使用夹具；不执行播放器、游戏进程或渲染器。实际完整跳过与失败不出奖励牌仍须配对实机验收。
