#!/usr/bin/env python3
"""Interactive entry for the standalone AT-5T state sampler."""

from datetime import datetime
from pathlib import Path
import sys
import time

import at5t_state_sampler as sampler


def main() -> int:
    if len(sys.argv) > 1:
        return sampler.main()

    print("AT-5T 上机状态采集")
    print("请先进入任务 3529，站到机甲附近。")
    print("在任务管理器的‘详细信息’中查看这次测试的 DNF.exe PID。")
    try:
        target_pid = sampler.positive_pid(input("输入该 PID：").strip())
        folder = Path(sys.executable).parent if getattr(sys, "frozen", False) else Path.cwd()
        reports = folder / "reports"
        reports.mkdir(exist_ok=True)
        output = reports / ("at5t-" + datetime.now().strftime("%Y%m%d-%H%M%S-%f") + ".jsonl")
        print("5 秒后开始采集，持续 20 秒。请切回游戏，松开 Z，再单按 Z。")
        print("等动作结束后再试两次，最后回到本窗口。")
        for remaining in range(5, 0, -1):
            print(f"{remaining}...", flush=True)
            time.sleep(1)
        result = sampler.run_sampler(target_pid, output)
        print(f"采集结束：{result['sample_count']} 条，状态 {result['stop_reason']}。")
        print(f"结果文件：{output}")
        code = 0
    except KeyboardInterrupt:
        print("已取消。")
        code = 2
    except (ValueError, OSError, sampler.SamplerError) as exc:
        print(f"采集未完成：{exc}")
        code = 2
    except Exception as exc:
        # argparse.ArgumentTypeError is raised by the shared PID parser.
        print(f"采集未完成：{type(exc).__name__}: {exc}")
        code = 2
    try:
        input("按回车关闭窗口。")
    except (EOFError, KeyboardInterrupt):
        pass
    return code


if __name__ == "__main__":
    raise SystemExit(main())
