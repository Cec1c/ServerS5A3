"""Remote test entrypoint: dedicated VPN plus loopback-only SSH bootstrap/API."""
import json
from pathlib import Path
import re
import socket
import subprocess
import time
import urllib.request

import local_client

ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / "runtime/remote"


def connect():
    config = json.loads((RUNTIME / "client.json").read_text(encoding="utf-8"))
    host = config["ssh_host"]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", host):
        raise ValueError("Invalid SSH alias")
    ports = [config["bootstrap_port"], config["api_port"]]
    if len(set(ports)) != 2 or any(type(p) is not int or not 1024 <= p <= 65535 for p in ports):
        raise ValueError("Invalid local tunnel ports")
    for port in ports:
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", port))
    flags = subprocess.CREATE_NO_WINDOW
    common = ["-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"]
    subprocess.run(["scp", *common, host + ":/opt/servers5a3/runtime/docker/data/manifest.json",
                    str(RUNTIME / "manifest.json")], check=True, creationflags=flags)
    manifest = json.loads((RUNTIME / "manifest.json").read_text(encoding="utf-8"))
    with socket.create_connection((config["vpn_ip"], manifest["ports"][0]), timeout=10):
        pass
    log = (RUNTIME / "ssh-tunnel.log").open("w", encoding="utf-8")
    tunnel = subprocess.Popen(["ssh", *common, "-N", "-T", "-o", "ExitOnForwardFailure=yes",
                               "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
                               "-L", f"127.0.0.1:{ports[0]}:127.0.0.1:7001",
                               "-L", f"127.0.0.1:{ports[1]}:127.0.0.1:17002", host],
                              stdin=subprocess.DEVNULL, stdout=log, stderr=log, creationflags=flags)
    log.close()
    api = f"http://127.0.0.1:{ports[1]}"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        for _ in range(30):
            if tunnel.poll() is not None:
                raise RuntimeError("SSH tunnel exited; see runtime/remote/ssh-tunnel.log")
            try:
                with opener.open(api + "/api/status", timeout=2) as response:
                    if response.status == 200:
                        return config, api, tunnel
            except OSError:
                time.sleep(.5)
        raise RuntimeError("Remote account service is not ready")
    except BaseException:
        tunnel.terminate()
        tunnel.wait(timeout=10)
        raise


def main():
    config, api, tunnel = connect()
    try:
        processes = local_client.main(RUNTIME / "client.json", api, config["bootstrap_port"],
                                      RUNTIME / "manifest.json", remote=True)
        # Closing the login window must not cut an active game's bootstrap connection.
        for process in processes:
            process.wait()
    finally:
        tunnel.terminate()
        tunnel.wait(timeout=10)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        local_client.messagebox.showerror("90cn 远端连接失败", str(error) + "\n请检查 WireGuard 隧道和 SSH 连接。")
        raise SystemExit(1)
