"""Reproducible, private-interface Docker profile. Requires Python 3.10+ and Docker."""
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / "runtime" / "docker"
IMAGE = "servers5a3-local:dev"
PROJECT = "servers5a3-local"


def run(*args, capture=False):
    return subprocess.run(list(map(str, args)), check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(value, encoding="utf-8", newline="\n")
    path.chmod(0o600)


def digest(path):
    sha = hashlib.sha256()
    with path.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""):
            sha.update(data)
    return sha.hexdigest()


def private_ipv4(value):
    address = ipaddress.IPv4Address(value)
    if not any(address in ipaddress.ip_network(n) for n in
               ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")):
        raise ValueError("Use an RFC1918 private LAN IPv4 for this local profile")
    return str(address)


def detect_ip():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as connection:
        connection.connect(("192.0.2.1", 9))  # Routing lookup; sends no packet.
        return private_ipv4(connection.getsockname()[0])


def build():
    run("docker", "build", "-t", IMAGE, "-f", ROOT / "deploy/docker/Dockerfile", ROOT)


def compose(*args):
    run("docker", "compose", "-p", PROJECT, "-f", RUNTIME / "compose.json", *args)


def management_token(c):
    # Validate before preparation touches assets, instance.json or Compose.
    if c.get("gm_enabled", False) is not False:
        raise ValueError("GM moved to https://github.com/Cec1c/DNF90GM; migrate it separately before regenerating this profile")
    token = c.get("player_admin_secret", "")
    if not isinstance(token, str) or (token and not re.fullmatch(r"[0-9a-f]{32,128}", token)):
        raise ValueError("player_admin_secret must be a generated hex secret of at least 32 characters")
    return token


def check_network(config):
    subnet = ipaddress.ip_network(config["subnet"])
    private_ipv4(str(subnet.network_address))
    for key in ("server_ip", "database_ip"):
        ip = ipaddress.ip_address(config[key])
        if ip not in subnet or ip in (subnet.network_address, subnet.broadcast_address):
            raise ValueError(f"{key} is not a usable address in subnet")
    if config["server_ip"] == config["database_ip"]:
        raise ValueError("Server and database require distinct addresses")
    ids = run("docker", "network", "ls", "-q", capture=True).split()
    if ids:
        for network in json.loads(run("docker", "network", "inspect", *ids, capture=True)):
            if network["Name"] == PROJECT + "_private":
                continue
            for item in network["IPAM"].get("Config") or []:
                other = ipaddress.ip_network(item["Subnet"])
                if other.version == 4 and subnet.overlaps(other):
                    raise ValueError(f"Docker subnet overlaps {network['Name']}; edit instance.json")


def prepare(args):
    instance = RUNTIME / "instance.json"
    if instance.exists():
        config = json.loads(instance.read_text(encoding="utf-8"))
        if args.host_ip:
            config["host_ip"] = private_ipv4(args.host_ip)
        if args.client_dir:
            config["client_dir"] = str(Path(args.client_dir).resolve())
    else:
        config = {
            "profile": "docker-local-token-v1",
            "host_ip": private_ipv4(args.host_ip) if args.host_ip else detect_ip(),
            "subnet": "10.253.90.0/24", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3",
            "database_password": secrets.token_hex(24), "root_password": secrets.token_hex(24),
            "admin_token": secrets.token_hex(32), "joust_event_enabled": True,
            "client_dir": str(Path(args.client_dir).resolve()) if args.client_dir else "",
            "body_codec": "plaintext", "client_body_codec": "plaintext",
            "default_group_login_only": True,
        }
    management_token(config)
    private_ipv4(config["host_ip"])
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind((config["host_ip"], 0))
    for key in ("database_password", "root_password", "admin_token"):
        if not config.get(key) or any(ch not in "0123456789abcdef" for ch in config[key]):
            raise ValueError(f"{key} must remain a non-empty generated hex value")
    check_network(config)
    assets = RUNTIME / "data"
    assets.mkdir(parents=True, exist_ok=True)
    target = assets / "Script.pvf"
    if args.pvf:
        source = Path(args.pvf).resolve(strict=True)
        if target.exists() and digest(source) != digest(target):
            raise ValueError("Existing PVF differs; back it up and explicitly remove the old copy first")
        if not target.exists():
            shutil.copyfile(source, target)
    if not target.is_file():
        raise ValueError("Run prepare --pvf <client/Script.pvf> first")
    target.chmod(0o644)
    if args.channel_info:
        source = Path(args.channel_info).resolve(strict=True)
        override = assets / "channel.override.etc"
        if override.exists() and digest(source) != digest(override):
            raise ValueError("Existing external channel table differs; preserve it before replacing")
        if not override.exists():
            shutil.copyfile(source, override)
        override.chmod(0o644)
        config["channel_file"] = override.name
        config["channel_source"] = str(source)
    # Decode the PVF or explicitly selected external table with the server's library.
    user = f"{os.getuid()}:{os.getgid()}" if os.name != "nt" else "0:0"
    channel_file = config.get("channel_file", "channel_info.etc")
    if channel_file not in ("channel_info.etc", "channel.override.etc"):
        raise ValueError("Unrecognized channel_file in instance.json")
    channel_args = ["-channel", "/data/" + channel_file] if channel_file == "channel.override.etc" else []
    report = run("docker", "run", "--rm", "--network", "none", "--user", user,
                 "--mount", f"type=bind,source={assets},target=/data", "--entrypoint", "DNF90Assets",
                 config.get("server_image", IMAGE), "-pvf", "/data/Script.pvf", "-output", "/data/" + channel_file, *channel_args, capture=True)
    catalog = json.loads(report)
    (assets / channel_file).chmod(0o644)
    catalog["pvf_sha256"] = digest(target)
    write(assets / "manifest.json", json.dumps(catalog, indent=2) + "\n")
    write(instance, json.dumps(config, indent=2) + "\n")
    generate(config, catalog)
    print(f"Prepared {catalog['channels']} real channels; configuration: {instance}")


def generate(c, catalog):
    solo_create = c.get("guild_solo_create", False)
    if type(solo_create) is not bool:
        raise ValueError("guild_solo_create must be a boolean")
    advertised_server = c.get("channel_advertise_server", 0)
    if type(advertised_server) is not int or not 0 <= advertised_server < 250:
        raise ValueError("channel_advertise_server must be an integer from 0 to 249")
    multiplier = c.get("main_quest_exp_multiplier")
    if "main_quest_exp_multiplier" in c and (type(multiplier) is not int or not 1 <= multiplier <= 100):
        raise ValueError("main_quest_exp_multiplier must be an integer from 1 to 100")
    game_token = management_token(c)
    server = RUNTIME / "server"
    for directory in (server / "logs", server / "configs/dnf", server / "configs/servergroup"):
        directory.mkdir(parents=True, exist_ok=True)
    dsn = f"dnf:{c['database_password']}@tcp({c['database_ip']}:3306)/dnf_local?parseTime=true&charset=utf8mb4,utf8"
    q = json.dumps
    config = f'''[service]
name = "dnfbridge"
node_id = "dnfbridge-docker-1"
environment = "local"
public_addr = "{c['host_ip']}:7001"
private_addr = "{c['server_ip']}:18111"
[cluster]
id = "local"
gid = 2
virtual_gid = 2
shard_id = 9999
zone = "local"
version = "local"
data_version = "local"
[admin]
listen = "{c['server_ip']}:18111"
token = {q(c['admin_token'])}
[pvf]
enabled = true
path = "data/dnf/Script.pvf"
max_bytes = 536870912
preload_chunks = false
[server_group]
enabled = true
store_kind = "file"
plan_file = "configs/servergroup/plan.json"
[topology]
enabled = false
[worker]
size = 8
queue = 256
[profile_store]
store_kind = "memory"
'''
    for section in ("idempotency", "registry", "bus", "cache", "lock", "presence"):
        config += f'[{section}]\nkind = "memory"\n'
    write(server / "configs/dnfbridge.toml", config)
    write(server / "configs/dnf/logic.toml", f'''[repository]
enabled = true
mysql_dsn = {q(dsn)}
mysql_max_open_conns = 32
mysql_max_idle_conns = 8
mysql_conn_max_lifetime_seconds = 300
shard_id = "9999"
table_prefix = "dnf"
server_group_plan_file = "configs/servergroup/plan.json"
auto_create_schema = true
csharp_legacy_schema = true
create_databases = false
''')
    plan = {"version": "dnf90-docker-v1",
            "shards": [{"id": "9999", "group_id": "logic-local-1", "state": "open",
                        "open_at": "2026-01-01T00:00:00Z", "public_open_at": "2026-01-01T00:00:00Z"}],
            "groups": [{"id": "logic-local-1", "service": "logic", "member_id": "logic-local-1", "state": "open"}],
            "routes": [{"feature": "dnf_repository", "shard_id": "9999", "group_id": "logic-local-1",
                        "state": "open", "meta": {"dnf.repository.write_database": "dnf_local",
                                                   "dnf.repository.read_database": "dnf_local"}}]}
    write(server / "configs/servergroup/plan.json", json.dumps(plan, indent=2))
    env = {
        "DNFBRIDGE_GUILD_SOLO_CREATE": str(solo_create).lower(),
        "DNFBRIDGE_CHANNEL_LISTEN": c["server_ip"] + ":7001",
        "DNFBRIDGE_GAME_LISTEN_HOST": c["server_ip"], "DNFBRIDGE_SERVER_IP": c["host_ip"],
        "DNFBRIDGE_CHANNEL_INFO_PATH": "data/dnf/" + c.get("channel_file", "channel_info.etc"), "DNFBRIDGE_PVF_PATH": "data/dnf/Script.pvf",
        "DNFBRIDGE_CHANNEL_INFO_BODY_MODE": "latest", "DNFBRIDGE_CHANNEL_SERVER_INDEX": "1",
        "DNFBRIDGE_CHANNEL_ADVERTISE_SERVER_INDEX": str(advertised_server), "DNF_SCRIPT_VERSION": "59",
        "DNFBRIDGE_CURRENT_CLIENT_VERSION": c.get("client_version", "official-24.9"),
        "DNFBRIDGE_DEFAULT_GROUP_LOGIN_ONLY": str(c.get("default_group_login_only", True)).lower(),
        "DNFBRIDGE_GAME_UPPER_HEADER": "server16", "DNFBRIDGE_GAME_UPPER_BODY_CODEC": c["body_codec"],
        "DNFBRIDGE_GAME_UPPER_CLIENT_BODY_CODEC": c["client_body_codec"], "DNFBRIDGE_DPROTO_MODE": "legacy_patch",
        "DNFBRIDGE_PARTY_UDP_RELAY_MODE": "embedded", "DNFBRIDGE_PARTY_UDP_RELAY_PORT_START": "30000",
        "DNFBRIDGE_PARTY_UDP_RELAY_PORT_COUNT": "64", "DNFBRIDGE_ACCOUNT_API_LISTEN": c["server_ip"] + ":17002",
        "DNFBRIDGE_ACCOUNT_API_DSN": dsn, "DNFBRIDGE_ACCOUNT_API_ALLOW_REGISTER": "true",
        "DNFBRIDGE_ACCOUNT_API_LOGIN_TOKEN": "true", "DNFBRIDGE_PACKET_LOG": str(c.get("packet_log", False)).lower(),
        "DNFBRIDGE_JOUST_EVENT_ENABLED": str(c["joust_event_enabled"]).lower(),
        "GOMEMLIMIT": c.get("go_memory_limit", "3500MiB"),
        "TZ": c.get("timezone", "Asia/Shanghai"),
    }
    if game_token:
        env["DNFBRIDGE_PLAYER_ADMIN_SECRET"] = game_token
    if multiplier is not None:
        env["DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER"] = str(multiplier)
    write(RUNTIME / "server.env", "".join(f"{k}={v}\n" for k, v in env.items()))
    write(RUNTIME / "database.env", f"MARIADB_DATABASE=dnf_local\nMARIADB_USER=dnf\nMARIADB_PASSWORD={c['database_password']}\nMARIADB_ROOT_PASSWORD={c['root_password']}\n")
    logging = {"driver": "json-file", "options": {"max-size": "20m", "max-file": "3"}}
    spec = {
        "services": {
            "db": {"image": c.get("database_image", "mariadb:11.8@sha256:be1ef4fe5f14589325c08a41c76334097ce66c86264b75e1d28342c742782a61"),
                   "env_file": ["database.env"], "volumes": ["database:/var/lib/mysql"],
                   "command": ["--bind-address=" + c["database_ip"], "--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci"],
                   "networks": {"private": {"ipv4_address": c["database_ip"]}}, "logging": logging,
                   "healthcheck": {"test": ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"], "interval": "5s", "timeout": "5s", "retries": 30}},
            "server": {"image": c.get("server_image", IMAGE), "init": True, "stop_grace_period": "45s",
                       "user": f"{os.getuid()}:{os.getgid()}" if os.name != "nt" else "10001:10001",
                       "env_file": ["server.env"], "volumes": ["./server:/runtime", "./data:/runtime/data/dnf:ro"],
                       "depends_on": {"db": {"condition": "service_healthy"}}, "mem_limit": c.get("server_memory_limit", "4g"), "logging": logging,
                       "networks": {"private": {"ipv4_address": c["server_ip"]}},
                       "ports": [f"127.0.0.1:{p}:{p}/tcp" for p in (7001, 17002, 18111)] +
                                [f"{c['host_ip']}:{p}:{p}/tcp" for p in catalog["ports"]] +
                                [f"{c['host_ip']}:30000-30063:30000-30063/udp"],
                       "healthcheck": {"test": ["CMD", "wget", "-Y", "off", "-q", "-O", "/dev/null", f"http://{c['server_ip']}:17002/api/status"],
                                       "start_period": "180s", "interval": "10s", "timeout": "5s", "retries": 12}},
        },
        "networks": {"private": {"ipam": {"config": [{"subnet": c["subnet"]}]}}},
        "volumes": {"database": {}},
    }
    if c.get("database_memory_limit"):
        spec["services"]["db"]["mem_limit"] = c["database_memory_limit"]
    for service in spec["services"].values():
        service["restart"] = c.get("restart", "no")
    write(RUNTIME / "compose.json", json.dumps(spec, indent=2) + "\n")


def main():
    global RUNTIME, PROJECT, IMAGE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "build", "up", "down", "status", "logs"))
    parser.add_argument("--pvf")
    parser.add_argument("--host-ip")
    parser.add_argument("--client-dir")
    parser.add_argument("--channel-info", help="Explicit external channel table; preserves the PVF-extracted table")
    parser.add_argument("--runtime-dir", type=Path, default=RUNTIME)
    parser.add_argument("--project", default=PROJECT)
    parser.add_argument("--image", default=IMAGE)
    args = parser.parse_args()
    RUNTIME, PROJECT, IMAGE = args.runtime_dir.resolve(), args.project, args.image
    if args.action == "build":
        build()
    elif args.action == "prepare":
        prepare(args)
    elif args.action == "up":
        prepare(args)
        compose("up", "-d", "--wait", "--wait-timeout", "300")
    elif args.action == "down":
        compose("down")  # Never remove the database volume.
    elif args.action == "status":
        compose("ps", "-a")
    else:
        compose("logs", "--tail", "100")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"Deployment stopped: {error}", file=sys.stderr)
        sys.exit(1)
