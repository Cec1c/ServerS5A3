## v168 当前成员与战况行保护候选

当前候选由 `build_v168.py` 从已发布 v168 构建，输入 SHA256 固定为 `4474c31236f2f38de1c772f0ac7f1f267d5d52b9dbed33c4925c1df91353cc62`。历史 v167 构建器保留供复现，不能用于当前玩家更新。

仅在原场景角色查询为空时，校验当前团本编号、当前成员名单、战况表对应副本行及该组首个成员身份，再查 category1 缓存。缓存角色须类型、角色号、owner和route完全相符，模型指针为空、职业落在现有16帧资源范围内。已有场景角色分支保持原路径，不创建场景角色，不调用会分配新团本状态的 getter。

新增代码在独立只读/可执行节内，除原条件跳转和必要PE头外保留v168所有字节，包括此前队员退图、公会及跨场景修复。使用原生职业头像分派，资源为 `character/defaultfaces.img`；匹配素材的16帧均有内容。资源内容摘要为 `5c886d535d80825fec982b0ed042a38df1340190defec7f895ddb8bbadd3fe53`，资源本体不加入仓库。

```text
python build_v168.py --input <v168.exe> --output-dir <新的候选目录>
python verify_v168.py --original <v168.exe> --candidate <候选目录>/v168-p34-guarded-candidate.exe --manifest <候选目录>/changes.json --report <新的回执.json>
```

验证读取实际候选代码，覆盖72项成员/行/类型/空模型/寄存器与栈约定，以及64项原生头像分派、首次资源加载/复用和共享引用清理。map查找末端、资源获取、帧获取与GPU边界使用明确夹具；完整游戏绘制、窗口反复开关和双端验收另记。生成候选不执行安装、发行或部署。

---

# 团本战况表的缓存角色头像候选

团本成员不在本场景时，战况表原来的行路径只查询当前场景角色，查询为空便跳过头像。此候选只在该空分支查询当前团本命名空间的 category 1 角色缓存，校验类型、角色 key、owner/route 和绘制条件后交还原有头像路径；查不到则保留原空分支。在场角色路径保持不变。

输入必须是 v167 EXE，SHA256 `922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b`。builder 不联网、不安装，也不覆盖输入或现有输出目录。仅改一条六字节 JE 的目标，追加 `.rport` 只读可执行代码节并更新必要的 PE 元数据；逐字节检查其他原文件内容、节布局、对齐和校验和。

```text
python scripts/client/raid-cached-portrait/build_candidate.py --input path/to/DNF.exe --output-dir path/to/new-candidate
```

输出为 `peer-v167-p34-candidate.exe` 和 `changes.json`。默认构建产物 SHA256 为 `8663331cf2e217185dfa220a23e9fce4688a69d413f66eb34736ae347d15fd9a`。

候选不分配或注册角色，也不修改缓存 actor。原生查询和渲染局部路径已有静态及有界控制流验证；游戏内资源生命周期、过期战况表与当前团本关联、反复打开窗口的稳定性仍需实机复测，不能据此认定 P34 已修复。复测应由另一个玩家查看单人副本成员头像，再测试换图、重开战况表、离团及重入；已有场景角色和本人头像应继续正常。

实际候选的有界 Unicorn 检查覆盖 40 个场景，包括原非空分支、精确缓存命中、错类别/角色/owner/route、缺失上下文、绘制门槛、调用者栈与 EFLAGS 保持。脚本执行 EXE 中的实际指令，只替代底层 map find/end；场景对象为合成夹具，执行止于原行处理续点。需要本地 Unicorn 2.1.4：

```text
python scripts/client/raid-cached-portrait/verify_packaged.py --input path/to/new-candidate/peer-v167-p34-candidate.exe --manifest path/to/new-candidate/changes.json --report path/to/new-verification.json
```
