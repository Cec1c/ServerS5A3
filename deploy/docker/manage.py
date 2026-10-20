"""Reproducible local/private and opt-in public remote Docker profiles."""
import argparse
import hashlib
import ipaddress
import itertools
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

RUNTIME_COMPATIBILITY_VALUES = {
    "DNFBRIDGE_MYSQL_INSERT_BATCH": None,
    "DNFBRIDGE_INVENTORY_SAVE_SCOPE": ("collection", "slot"),
    "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE": ("collection", "entry"),
    "DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL": ("on",),
    "DNFBRIDGE_PARTY_SELECTOR_MEMBER_SELF_PREPARE": ("on",),
    "DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE": ("all",),
    "DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF": ("room_proven",),
    "DNFBRIDGE_HELL_BLOCKED_SLOTS": ("on",),
    "DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE": ("allow",),
    "DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD": ("on",),
    "DNFBRIDGE_TOWN_NPC_EVENT_CATALOG": ("on",),
    "DNFBRIDGE_PARTY_FORMATION_ORDER": ("vendor_wire",),
    "DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER": ("repeat",),
    "DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST": ("on",),
    "DNFBRIDGE_LOGIN_AREA_GATES": ("strict",),
    "DNFBRIDGE_HOTKEY_SEEDED_LAYOUT": ("off",),
    "DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS": ("off",),
    "DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT": ("before_transition_only",),
    "DNFBRIDGE_PEER_SKILL_PAGE_TOWN": ("off",),
    "DNFBRIDGE_SKILL_SWITCH_DOWNLINK_SHAPE": ("off",),
    "DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY": ("off",),
}


def runtime_compatibility_env(config):
    compatibility = config.get("runtime_compatibility", {})
    if compatibility == {}:
        return {}
    if not isinstance(compatibility, dict):
        raise ValueError("runtime_compatibility must be an object")
    validated = {}
    for key, value in compatibility.items():
        if key not in RUNTIME_COMPATIBILITY_VALUES:
            raise ValueError(f"runtime_compatibility contains unsupported key: {key}")
        if not isinstance(value, str):
            raise ValueError(f"runtime_compatibility value must be a string: {key}")
        allowed = RUNTIME_COMPATIBILITY_VALUES[key]
        if allowed is None:
            if not re.fullmatch(r"[0-9]+", value) or not 1 <= int(value) <= 1000:
                raise ValueError("DNFBRIDGE_MYSQL_INSERT_BATCH must be an integer from 1 to 1000")
        elif value not in allowed:
            raise ValueError(f"runtime_compatibility value is not allowed: {key}={value}")
        validated[key] = value
    return validated


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


def player_endpoints(config, catalog):
    """Cloud NAT binds a real private NIC; only advertisements use its public IP."""
    bind = private_ipv4(config["host_ip"])
    advertised = config.get("advertised_ip", bind)
    public = config.get("profile") == "docker-remote-public-v1"
    if public:
        address = ipaddress.IPv4Address(advertised)
        if not address.is_global or address.is_multicast:
            raise ValueError("Public remote profile requires a globally routable advertised_ip")
        if not config.get("player_strict") or not config.get("player_proxy_secret") or not config.get("player_admin_secret"):
            raise ValueError("Public remote profile requires strict player authentication and private gateway secrets")
    elif advertised != bind:
        raise ValueError("Separate advertised_ip requires the explicit public remote profile")
    bootstrap = config.get("bootstrap_port", 10000 if public else 27001)
    if type(bootstrap) is not int or not 1024 <= bootstrap <= 65535 or bootstrap in catalog["ports"] or bootstrap in (7001, 17002, 18111, 3306):
        raise ValueError("bootstrap_port must be an unused, non-privileged player port")
    return bind, str(advertised), bootstrap


def build():
    run("docker", "build", "-t", IMAGE, "-f", ROOT / "deploy/docker/Dockerfile", ROOT)


def build_gm(image):
    run("docker", "build", "-t", image, "-f", ROOT / "deploy/docker/Dockerfile.gm", ROOT)


def gm_settings(c, catalog, port):
    if port is None:
        return None
    token = c.get("player_admin_secret") or c["admin_token"]
    if not isinstance(token, str) or not re.fullmatch(r"[0-9a-f]{32,128}", token):
        raise ValueError("Independent GM requires a generated private game admin secret")
    checksum = catalog.get("pvf_sha256", "")
    if not re.fullmatch(r"[0-9a-fA-F]{64}", checksum):
        raise ValueError("Independent GM requires the actual PVF manifest SHA256")
    network = ipaddress.ip_network(c["subnet"])
    reserved = {ipaddress.ip_address(c[key]) for key in ("server_ip", "database_ip")}
    reserved.add(next(network.hosts()))
    candidate = c.get("gm_ip") or next((str(ip) for ip in itertools.islice(network.hosts(), 1, 32) if ip not in reserved), None)
    if candidate is None:
        raise ValueError("No separate GM address available in subnet")
    address = ipaddress.ip_address(private_ipv4(candidate))
    if address not in network or address in reserved or address in (network.network_address, network.broadcast_address):
        raise ValueError("gm_ip must be a distinct usable address in subnet")
    dsn = f"dnf:{c['database_password']}@tcp({c['database_ip']}:3306)/dnf_local?parseTime=true&charset=utf8mb4&loc=UTC"
    return {"listen": f"{address}:{port}", "password": c["gm_password"],
            "registrationDsn": dsn, "repositoryDsn": dsn,
            "accountTable": "dnf90_web_accounts", "databases": ["dnf_local"], "tablePrefix": "dnf",
            "pvfPath": "/assets/Script.pvf", "pvfSha256": checksum,
            "itemIndexPath": "/runtime/item-index.json",
            "gameApi": f"http://{c['server_ip']}:17002/api/game/admin", "gameToken": token, "scanSeconds": 30}


def gm_port(config, catalog, bootstrap):
    """Opt-in GM stays on host loopback and reuses the private account DSN."""
    enabled = config.get("gm_enabled", False)
    if type(enabled) is not bool:
        raise ValueError("gm_enabled must be a boolean")
    if not enabled:
        return None
    password = config.get("gm_password", "")
    if not isinstance(password, str) or not re.fullmatch(r"[0-9a-f]{32,128}", password):
        raise ValueError("gm_password must be a generated hex secret of at least 32 characters")
    port = config.get("gm_port", 18112)
    reserved = {3306, 7001, 17002, 18111, bootstrap, *catalog["ports"]}
    if type(port) is not int or not 1024 <= port <= 65535 or port in reserved or 30000 <= port <= 30063:
        raise ValueError("gm_port must not overlap game, bootstrap or existing management ports")
    return port


def compose(*args):
    run("docker", "compose", "-p", PROJECT, "-f", RUNTIME / "compose.json", *args)


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
    owner_range = c.get("channel_directory_owner_range")
    if "channel_directory_owner_range" in c and owner_range not in ("off", "u8"):
        raise ValueError("channel_directory_owner_range must be off or u8")
    percent = c.get("main_quest_exp_percent")
    if "main_quest_exp_percent" in c and (type(percent) is not int or not 0 <= percent <= 100):
        raise ValueError("main_quest_exp_percent must be an integer from 0 to 100")
    multiplier = c.get("main_quest_exp_multiplier")
    if "main_quest_exp_multiplier" in c and (type(multiplier) is not int or not 1 <= multiplier <= 100):
        raise ValueError("main_quest_exp_multiplier must be an integer from 1 to 100")
    tournament = c.get("dungeon_tournament")
    if "dungeon_tournament" in c and tournament not in ("off", "vendor"):
        raise ValueError("dungeon_tournament must be off or vendor")
    solo_create = c.get("guild_solo_create", False)
    if type(solo_create) is not bool:
        raise ValueError("guild_solo_create must be a boolean")
    buff_swap_reapply = c.get("buff_swap_reapply")
    if buff_swap_reapply is not None and buff_swap_reapply not in ("on", "off"):
        raise ValueError("buff_swap_reapply must be 'on' or 'off' when configured")
    compatibility_env = runtime_compatibility_env(c)
    advertised_server = c.get("channel_advertise_server", 0)
    if type(advertised_server) is not int or not 0 <= advertised_server < 250:
        raise ValueError("channel_advertise_server must be an integer from 0 to 249")
    bind, advertised, bootstrap = player_endpoints(c, catalog)
    management_port = gm_port(c, catalog, bootstrap)
    gm = gm_settings(c, catalog, management_port)
    public_port = bootstrap if c.get("profile") == "docker-remote-public-v1" else 7001
    server = RUNTIME / "server"
    for directory in (server / "logs", server / "configs/dnf", server / "configs/servergroup"):
        directory.mkdir(parents=True, exist_ok=True)
    dsn = f"dnf:{c['database_password']}@tcp({c['database_ip']}:3306)/dnf_local?parseTime=true&charset=utf8mb4,utf8"
    q = json.dumps
    config = f'''[service]
name = "dnfbridge"
node_id = "dnfbridge-docker-1"
environment = "local"
public_addr = "{advertised}:{public_port}"
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
        "DNFBRIDGE_GAME_LISTEN_HOST": c["server_ip"], "DNFBRIDGE_SERVER_IP": advertised,
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
        "DNFBRIDGE_PLAYER_STRICT": str(c.get("player_strict", False)).lower(),
        "DNFBRIDGE_PLAYER_ADMIN_SECRET": c.get("player_admin_secret", ""),
        "DNFBRIDGE_PLAYER_PROXY_SECRET": c.get("player_proxy_secret", ""),
        "DNFBRIDGE_PLAYER_REGISTRATION_LIMIT": str(c.get("player_registration_limit", 0)),
        "DNFBRIDGE_PLAYER_MAX_ONLINE": str(c.get("player_max_online", 0)),
    }
    if buff_swap_reapply is not None:
        env["DNFBRIDGE_BUFF_SWAP_REAPPLY"] = buff_swap_reapply
    if owner_range is not None:
        env["DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE"] = owner_range
    if tournament is not None:
        env["DNFBRIDGE_DUNGEON_TOURNAMENT"] = tournament
    if multiplier is not None:
        env["DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER"] = str(multiplier)
    if percent is not None:
        env["DNFBRIDGE_MAIN_QUEST_EXP_PERCENT"] = str(percent)
    if management_port is not None:
        env["DNFBRIDGE_PLAYER_ADMIN_SECRET"] = gm["gameToken"]
    env.update(compatibility_env)
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
                                [f"{bind}:{p}:{p}/tcp" for p in catalog["ports"]] +
                                [f"{bind}:30000-30063:30000-30063/udp"],
                       "healthcheck": {"test": ["CMD", "wget", "-Y", "off", "-q", "-O", "/dev/null", f"http://{c['server_ip']}:17002/api/status"],
                                       "start_period": "180s", "interval": "10s", "timeout": "5s", "retries": 12}},
        },
        "networks": {"private": {"ipam": {"config": [{"subnet": c["subnet"]}]}}},
        "volumes": {"database": {}},
    }
    if c.get("database_memory_limit"):
        spec["services"]["db"]["mem_limit"] = c["database_memory_limit"]
    if management_port is not None:
        write(RUNTIME / "gm/config.json", json.dumps(gm, indent=2) + "\n")
        (RUNTIME / "gm/state").mkdir(parents=True, exist_ok=True)
        spec["services"]["gm"] = {
            "image": c.get("gm_image", "servers5a3-gm:dev"), "init": True,
            "environment": {"GOMEMLIMIT": c.get("gm_go_memory_limit", "768MiB")},
            "user": spec["services"]["server"]["user"],
            "command": ["--config", "/config/gm.json", "--init-schema"],
            "volumes": ["./gm/config.json:/config/gm.json:ro", "./gm/state:/runtime", "./data/Script.pvf:/assets/Script.pvf:ro"],
            "networks": {"private": {"ipv4_address": gm["listen"].rsplit(":", 1)[0]}},
            "depends_on": {"db": {"condition": "service_healthy"}},
            "ports": [f"127.0.0.1:{management_port}:{management_port}/tcp"],
            "mem_limit": c.get("gm_memory_limit", "2g"), "logging": logging,
            "healthcheck": {"test": ["CMD", "wget", "-Y", "off", "-q", "-O", "/dev/null", f"http://{gm['listen']}/api/status"],
                            "start_period": "180s", "interval": "15s", "timeout": "5s", "retries": 12}}
    for service in spec["services"].values():
        service["restart"] = c.get("restart", "no")
    write(RUNTIME / "compose.json", json.dumps(spec, indent=2) + "\n")


def main():
    global RUNTIME, PROJECT, IMAGE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "build", "build-gm", "up", "up-gm", "down", "status", "logs"))
    parser.add_argument("--gm-image", default="servers5a3-gm:dev")
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
    elif args.action == "build-gm":
        build_gm(args.gm_image)
    elif args.action == "up-gm":
        # Configuration is reviewed/generated separately. Never restart game or DB.
        spec = json.loads((RUNTIME / "compose.json").read_text(encoding="utf-8"))
        if "gm" not in spec.get("services", {}):
            raise ValueError("Generate the independent GM configuration first")
        compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "300", "gm")
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
