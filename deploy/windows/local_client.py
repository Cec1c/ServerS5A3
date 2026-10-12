"""Independent local test launcher; never runs the vendor updater or modifies DNF.exe."""
import hashlib
import csv
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tkinter as tk
from tkinter import messagebox, ttk
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
SUPPORTED_EXE = "bc613e368e8fd49c50c35163f2ed8a28564fc23342701a5a6c22b839f3bf6b7a"
API = "http://127.0.0.1:17002"


def request(action, account, password, api=API):
    data = json.dumps({"account": account, "password": password}).encode()
    req = urllib.request.Request(api + "/api/account/" + action, data=data,
                                 headers={"Content-Type": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=15) as response:
            result = json.load(response)
    except urllib.error.HTTPError as error:
        result = json.load(error)
    if not result.get("ok"):
        raise ValueError(result.get("message", "Login failed"))
    return result


def launch(token, client_dir, bootstrap_port=7001, manifest_path=None):
    if os.name != "nt":
        raise ValueError("The game client requires Windows")
    if not re.fullmatch(r"[a-f0-9]{32}", token or ""):
        raise ValueError("The server did not issue a valid login token")
    client = Path(client_dir).resolve(strict=True)
    executable = client / "DNF.exe"
    processes = subprocess.run(["tasklist", "/FI", "IMAGENAME eq DNF.exe", "/FO", "CSV", "/NH"],
                               capture_output=True, text=True, errors="replace", creationflags=subprocess.CREATE_NO_WINDOW, check=True)
    if any(row and row[0].lower() == "dnf.exe" for row in csv.reader(io.StringIO(processes.stdout))):
        raise ValueError("DNF.exe is already running; close the existing client first")
    with executable.open("rb") as stream:
        digest = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != SUPPORTED_EXE:
        raise ValueError("DNF.exe differs from the inspected v56 client; verify its launch protocol first")
    if not isinstance(bootstrap_port, int) or not 1024 <= bootstrap_port <= 65535:
        raise ValueError("Invalid bootstrap port")
    manifest_path = manifest_path or ROOT / "runtime/docker/data/manifest.json"
    manifest = json.loads(Path(manifest_path).read_text(encoding="utf-8"))
    with (client / "Script.pvf").open("rb") as stream:
        digest = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != manifest["pvf_sha256"]:
        raise ValueError("Client and server PVF hashes differ; prepare matching assets before testing")
    # Derived from this client's launcher StartGame argument contract.
    arguments = f"99?127.0.0.1?{bootstrap_port}?0?" + token + "?01?1?0?0?0?0?1?9n2b1c8r3w7y?0?0?19847"
    return subprocess.Popen([str(executable), arguments], cwd=client)


def main(config_path=None, api=API, bootstrap_port=7001, manifest_path=None, remote=False):
    processes = []
    config_path = config_path or ROOT / "runtime/docker/instance.json"
    title = "ServerS5A3 远端测试 · 90cn" if remote else "ServerS5A3 本地测试"
    window = tk.Tk()
    window.title(title)
    window.resizable(False, False)
    panel = ttk.Frame(window, padding=24)
    panel.grid()
    ttk.Label(panel, text="腾讯云 90cn · 远端测试" if remote else "Docker Linux · 本机测试", font=("Microsoft YaHei UI", 14)).grid(row=0, columnspan=2, pady=(0, 16))
    ttk.Label(panel, text="账号").grid(row=1, column=0, padx=(0, 12), pady=6)
    account = ttk.Entry(panel, width=28)
    account.grid(row=1, column=1)
    ttk.Label(panel, text="密码").grid(row=2, column=0, padx=(0, 12), pady=6)
    password = ttk.Entry(panel, show="*", width=28)
    password.grid(row=2, column=1)
    status = tk.StringVar(value="远端连接已就绪，可以登录已有账号。" if remote else "请先启动 Docker 服务端，再注册或登录。")
    ttk.Label(panel, textvariable=status, wraplength=320).grid(row=4, columnspan=2, pady=(14, 0))

    def submit(register=False):
        try:
            config = json.loads(Path(config_path).read_text(encoding="utf-8"))
            if register:
                request("register", account.get().strip(), password.get(), api=api)
                status.set("注册成功，可以登录并启动游戏。")
            else:
                result = request("login", account.get().strip(), password.get(), api=api)
                process = launch(result.get("token"), config["client_dir"], bootstrap_port, manifest_path)
                processes.append(process)
                password.delete(0, tk.END)
                status.set(f"游戏已启动（PID {process.pid}），等待客户端加载。")
        except Exception as error:
            messagebox.showerror(title + "启动失败", str(error))

    ttk.Button(panel, text="注册测试账号", command=lambda: submit(True)).grid(row=3, column=0, pady=(12, 0))
    ttk.Button(panel, text="登录并启动游戏", command=submit).grid(row=3, column=1, pady=(12, 0))
    account.focus()
    window.mainloop()
    return processes


if __name__ == "__main__":
    main()
