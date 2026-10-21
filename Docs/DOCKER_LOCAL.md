# Docker Linux local test environment

This profile builds the Go server on Linux and uses a dedicated MariaDB 11.8
database. Windows runs only the game client and local launcher. It requires
Docker Compose v2, Python 3.10+ (with Tk for the launcher), approximately 4 GiB
for the server, and matching client/PVF/channel assets supplied separately.

## Prepare and start

From the repository root on Windows:

```bat
DOCKER.bat build
DOCKER.bat prepare --pvf "D:\your-client\client\Script.pvf" --channel-info "go-server\internal\services\dnfbridge\testdata\channel_info.etc" --client-dir "D:\your-client\client" --host-ip 192.168.1.50
DOCKER.bat up
LOCAL_CLIENT.bat
```

After preparation, double-click `START_LOCAL.bat` to start Docker Desktop if
needed, wait for the database/server health checks, and open the local launcher.
Register your own test account, then select **登录并启动游戏**. No vendor launcher
or additional client DLL is required for the validated client below.

Use your actual private LAN IPv4, not the example address. `prepare` can detect
the default-route address if `--host-ip` is omitted. The `client-dir` must contain
`DNF.exe`. The launcher can register a fresh test account, authenticate it with
the local API and launch the unmodified executable with the returned token.
Passwords are not stored by the launcher. It rejects an already running client,
an unrecognized executable, or a client/server PVF hash mismatch.

The launcher is currently restricted to the inspected v56 `DNF.exe` SHA256
`bc613e368e8fd49c50c35163f2ed8a28564fc23342701a5a6c22b839f3bf6b7a`.
The original updater, its signing keys and its remote account service are not
used. This is an independent local test entrypoint, not a patched vendor launcher.

On Linux, use `sh deploy/docker/manage.sh` in place of `DOCKER.bat`. Build first,
then prepare using local asset paths. The game still requires Windows.

## Configuration and assets

`runtime/docker/instance.json` is authoritative for this separate profile.
`prepare` regenerates TOML, Compose and environment files under `runtime/docker`;
do not hand-edit the generated files. The old Windows controller and its
`runtime/config/instance.json` are separate and must not manage these containers.

Select the external channel table explicitly on the first `prepare` call.
The [channel deployment guide](CHANNEL_TABLE_DEPLOYMENT.md) includes the approved
118-row CSV and the matching complete ETC input. Preparation copies the supplied
PVF and selected table, records SHA256 values, and derives game ports from that
external catalog. Preserve it across PVF updates. The implementation can extract
a PVF table when no override is selected; that is not this deployment procedure.

An externally maintained channel table can be selected explicitly:

```bat
DOCKER.bat prepare --channel-info "D:\your-assets\channel_info.etc"
DOCKER.bat up
```

This preserves the PVF-extracted table, stages `channel.override.etc`, records
the source path and hash, and uses the external table for both the server
catalog and client download. A parser accepting a table does not prove that
the current game can use it. The linked repository table has been explicitly
matched to the supplied CSV; select it by path and verify its hash rather than
treating arbitrary test fixtures as deployment inputs.

For the inspected YKCN90 v56 client, the PVF's extracted table (38 server-1
channels) did not reach game login. Explicitly selecting the existing repository
reference (118 channels) allowed token-bound login, character creation and the
Lorain tutorial scene:

```bat
DOCKER.bat prepare --channel-info "go-server\internal\services\dnfbridge\testdata\channel_info.etc"
```

This is the table selected in the tested local environment. Its SHA256 is
`b1741336a9f91646ece865c799d187d6c36460bc826bf9efbc1b850b39a71a36`.
The supplied client/server PVF SHA256 is
`b86dd8fa5bcde40a445d9e494a9e1a89d0fc15ddd15d61041e4b97f78973d00f`.
These results establish basic startup compatibility, not complete gameplay or
raid asset coverage. No replacement database or old one-click package was used.

The Docker profile selects `official-24.9` (CMD 1579 / NOTI 1543), matching the
inspected executable, and accepts its observed 606-byte login body with the
account token at offset 9. It also defaults `default_group_login_only` to true:
default/Cain directory requests, including repeat requests without a script
download, return the catalog's ordinary resident channels with IDs at most 255.
Multiple real rows are retained for native auto-selection. This deliberately limits
the manual default-group directory too; named-group queries remain unchanged.
Set that option to false and run `up` to restore the full default directory.
The server outside this Docker profile retains the original default behavior.

Generated credentials, PVF files, logs and account data are ignored by Git.
Database state lives in the `servers5a3-local_database` Docker volume. Do not
publish `instance.json`, `.env` files, packet logs or database dumps.

## Network and operations

| Endpoint | Windows host binding |
| --- | --- |
| Channel bootstrap 7001/tcp | 127.0.0.1 |
| Account API 17002/tcp | 127.0.0.1 |
| Admin 18111/tcp | 127.0.0.1, token required |
| Catalog game ports | Configured private LAN IPv4 |
| Embedded UDP relay 30000–30063 | Configured private LAN IPv4 |
| MariaDB | No published port |

Inside the dedicated bridge network, services listen on fixed private container
IPs; the server advertises the host LAN address. The default subnet is
`10.253.90.0/24`; preparation rejects overlap with other Docker networks.
The UDP relay is embedded and uses the same separate bind/advertise addresses.
It does not use the upstream remote relay. Health checks bypass HTTP proxies.
The image embeds Go timezone data and defaults to `Asia/Shanghai` for the
date-dependent channel handshake; `timezone` can be set in `instance.json`.

```bat
DOCKER.bat status
DOCKER.bat logs
DOCKER.bat down
```

`down` removes only this project's containers and network, preserving its
database volume. There is no database reset command. Restart with `up`; rebuild
with `build` after source changes. Containers do not start automatically on
Docker Desktop launch. For packet diagnostics, set `packet_log` to true in
`instance.json`, run `up`, then restore it to false after investigation.

## Moving to a Linux test host

Clone the source, build the image on that host, supply the verified assets,
and run preparation with its private interface address. Create new credentials
for a fresh environment. For existing player data, take a logical MariaDB dump
and verify its restore into a new volume before switching clients.

This is a local/private-network profile. A remote Windows client needs a private
network route to the advertised game/UDP address and SSH forwarding for local
bootstrap/account endpoints, for example ports 7001 and 17002. Keep the same
assets on the client and test host. Internet-facing account registration, TLS,
multi-user authentication hardening, firewall policy and a public launcher are
separate work; merely replacing the private IP with a public IP is unsupported.

On the Windows launcher machine, keep a source checkout with Python/Tk and the
client. Place the test host's **asset manifest only** at
`runtime/docker/data/manifest.json`, and create
`runtime/docker/instance.json` containing just
`{"client_dir": "D:/your-client/client"}`. Keep the SSH forwards active and run
`LOCAL_CLIENT.bat`. Do not use `START_LOCAL.bat` for a remote host, because it
starts a local Docker deployment; do not copy database passwords to the client.

Current authentication retains the upstream legacy shared-account fallback for
game connections without a recognized token. Successful API login alone is
therefore insufficient: verify `account_from_token=true` for the actual game
connection before using this environment for multiplayer or private accounts.

## Validation limits

The supplied v56 executable and PVF were hash-checked. The actual Windows client
registered/logged in through the local API, bound the Linux game session to its
issued token, created `dockertest`, retained the character after container and
client restarts, and entered the Lorain tutorial with keyboard movement.
This is a startup smoke test, not a complete dungeon/combat/quest test.

The PVF currently references missing indexed maps (raid 5314 -> 292101788 and
raid 5323 -> 95267). Keep these asset compatibility issues open for subsequent
gameplay repair. Runtime logs and screenshots remain local, outside the source
repository; packet logging is disabled after diagnosis because raw packets can
contain login tokens.
