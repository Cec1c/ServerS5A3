# 小队奖励卡片门禁：原生窗口验证

`verify_candidate.py` 串行执行五组共 **620 个用例**，包含旧路径对照和同对象连续操作。验证器不调用构建器；只读取明确传入的两个 EXE，要求 Python 3.10+、`unicorn==2.1.4` 及匹配平台的本地库。可使用正常解释器环境、`PYTHONPATH`，或设置 `ROW_GATE_UNICORN_PATH` 指向包含 `unicorn/__init__.py` 的包父目录。

```text
python -B verify_candidate.py --base-client-exe ./DNF-v161.exe --candidate-exe ./DNF-row-gate-experiment.exe --output-dir ./verification-new
```

三个参数均必需。基线完整 SHA256 为 `5a13cd951aa246682f21466542d582901fc136bbf5578cc0ab88bb742c3b0dcc`（111890432 字节）；候选为 `04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad`（111894528 字节）。先验证摘要和依赖，再创建新的输出目录；拒绝已有目录、错误输入及优化模式。失败返回非零，保存诊断日志；仅全部通过才写 `summary.json`。

| 组 / 源文件 | 用例组成 | 真实执行范围 |
| --- | --- | --- |
| core / verify_core.py，429 | 33 变体×3 槽×4 入口，9 个旧路径对照，24 个同对象阶段 | 父弱转强/释放、DWORD 成员查找、namespace getter、原 render 与递归 input |
| render_path / verify_render_path.py，26 | 11 变体×旧/新，4 个同对象阶段 | 父子遍历、row tick、零时长运动与 nil callback 点击分支 |
| direct_input / verify_direct_input.py，17 | 6 变体×旧/新，5 个同对象阶段 | 独立 +18、原 3829F50/3829DC0/37E9ED0；隐藏卡不命中、不绘制 |
| completion / verify_completion.py，120 | 4 变体×5 状态×旧/新×3 绑定组合 | 8ED470 完成遍历、行数/共享 getter、190CC50 与 190C900 状态选择 |
| callback / verify_callback.py，28 | 6 变体×旧/新×2 入口，4 个同对象阶段 | 非 nil 点击消费、本人弱引用提升、cast wrapper 与 1F1FAF0 callable 分派 |

每组保存 JSON 收据及 stdout/stderr；汇总保存输入和源码摘要、依赖版本、fixture 清单。各 verify_*.py 也支持相同参数独立运行。模块导入不读取 EXE，不依赖原实验目录、v1 EXE 或构建收据。

`patched=False` 从基线读取 outer vtable，执行与基线相同的固定原生窗口；不是启动完整旧客户端。core 的 key/namespace 宽度、过期父引用、不同分类和无上下文等控制保持。曾只拦 +1C/+68 的旧实验漏掉独立 +18 先进行的命中，当前三入口候选关闭该缺口；五项历史反例不计入这里的 620 项，也不作为运行所需文件。

completion 用例分别区分本人行未绑定和另一根页面未绑定，保存唯一 caseId、输入与实际返回。每次 render、predicate、manual 后分别比较对象快照；timer 后确认仅预期计数增加，再清零该计数做最终比较。它证明暂停离队卡片动画没有改变所测完成判断和下一状态选择；190C6D0 的具体切页操作仍是末端 fixture。

callback 执行非空回调的原生提升、复制与释放链。RTTI 结果和最终 callable 是 fixture，末端按按值参数的所有权释放一份引用；门禁隐藏卡片时不进入回调，保留成员/重入后的卡片仍进入。没有执行最终奖励请求或异常展开。

所有用例都在合成对象上运行，迭代器、namespace 锁/解码、输入/点击、计时器、部分资源和绘图末端为 fixture。没有读取游戏进程、执行真实 OS 输入、GPU、完整 UI 资源生命周期或实机验收。原奖励槽位与坐标保留，不做物理压缩；构建和验证均不改奖励资格或发放资产。
