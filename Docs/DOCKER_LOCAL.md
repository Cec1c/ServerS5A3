# Docker Linux local test environment

This profile builds the Go server on Linux and uses a dedicated MariaDB 11.8
database. Windows runs only the game client and local launcher. It requires
Docker Compose v2, Python 3.10+ (with Tk for the launcher), approximately 4 GiB
for the server, and matching client/PVF/channel assets supplied separately.

## Prepare and start

From the repository root on Windows:

```bat
DOCKER.bat build
DOCKER.bat prepare --pvf "D:\your-client\client\Script.pvf" --client-dir "D:\your-client\client" --host-ip 192.168.1.50
DOCKER.bat up
LOCAL_CLIENT.bat
```

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

The preparation command copies the supplied PVF and extracts its real
`etc/channel_info.etc` using the server's decoder. It records SHA256 values and
derives published game ports from the parsed catalog. Existing assets with
different bytes are never silently replaced.

An externally maintained channel table can be selected explicitly:

```bat
DOCKER.bat prepare --channel-info "D:\your-assets\channel_info.etc"
DOCKER.bat up
```

This preserves the PVF-extracted table, stages `channel.override.etc`, records
the source path and hash, and uses the external table for both the server
catalog and client download. A parser accepting a table does not prove that
the current game can use it. The repository's test fixture is a reference for
diagnosis, not an automatically selected production asset.

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

Current authentication retains the upstream legacy shared-account fallback for
game connections without a recognized token. Successful API login alone is
therefore insufficient: verify `account_from_token=true` for the actual game
connection before using this environment for multiplayer or private accounts.
