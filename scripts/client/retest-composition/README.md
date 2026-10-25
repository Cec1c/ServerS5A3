# 完整客户端验收候选

以已发布 V175 为唯一输入，组合称号穿脱、离队成员退役、队伍血条和五组按类别接收门修复。四个原构建器分别读取同一输入，组合器拒绝重叠改动，只重新计算 PE 校验和。公会商人及此前累计补丁保留。

输入 SHA256：`c97fec768b7178ae89446a65de5aa980a1532724e3a1bde49e5273fe5aff0db6`。

输出 SHA256：`2a6cc14f3353e3d9d142b8580e432fe76ff3c1bbdc5be6818092347f9003bc74`。

```text
python scripts/client/retest-composition/build_candidate.py --input DNF-v175.exe --output DNF-candidate.exe --work-dir fresh-build-details --receipt build-receipt.json
```

输出与已完成有界原生验证的候选必须逐字节相同。构建成功不代表游戏验收完成；发布需要当次成功权威 pull、实际线上基线核对、双端 Release 读回、稳定通道与配套资产核验，随后才可部署依赖服务端。原 MR 保持完整范围，最终合并等待用户游戏验收。

构建回执只证明组合字节一致；完整原生与Windows映像验证由发布流程绑定相同SHA256的独立证据。输出、回执和工作目录必须使用不存在的新路径。若写入中断，保留该次产物供检查，下一次使用新路径；缺少成功回执或摘要不符的文件不可发布。
