"""Read-only historical bundle 008 identity contract; not a deployment controller."""

import argparse
import hashlib
import json
from pathlib import Path

RUNTIME_CHANNEL_TARGET = 'runtime/docker/data/channel.override.etc'

RUNTIME_CHANNEL_CONTAINER = '/runtime/data/dnf/channel.override.etc'

RUNTIME_CHANNEL_FILE = 'channel.override.etc'

BUNDLE008_CLIENT_FILES = {'DNF.exe': {'sha256': '534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24', 'bytes': 112541696}, 'ImagePacks2/sprite_interface2_cs_shop_cs_battlpass.NPK': {'sha256': '6f92cb8d880aa9887aada0a9046598c4a45188676ca6c6f63eb715a76e507ccc', 'bytes': 475234}, 'ImagePacks2/sprite_interface2_hud_information_button.NPK': {'sha256': '979c0ca41406efdf5bd40a882e9ac71103bee8151d1a281d7c8f15d2a0f033ce', 'bytes': 568229}, 'ImagePacks2/sprite_interface2_localaccountrank.NPK': {'sha256': '59f742a1eb1734b1352668ce5a2246ac8d7f36f78a3213ce6a7e946da7f3edd9', 'bytes': 16949}, 'ImagePacks2/sprite_interface2_localbattlepass_light95.NPK': {'sha256': '886179d9f49dac8d0e287e8712d398f70f45f005512c737c582209ffdc9b7555', 'bytes': 324248}, 'ImagePacks2/sprite_interface2_localbattlepass_windowcommon95.NPK': {'sha256': 'ff3d7e2bc51a3e68741ba118e8ff2bceaca6a8c4bb5b430156cc533ca060ce82', 'bytes': 177966}, 'ImagePacks2/sprite_interface_privateactivity010.NPK': {'sha256': '4fa1c2be179eacb3ef7c56a0b6e07c558b168b61cc3bb3468fcbb2a341df7840', 'bytes': 325146}, 'ImagePacks2/sprite_interface_privatefestival005.NPK': {'sha256': 'd92fede3d90b766bb1bde768f48d81c47af877f901066c13ce7d60d5ee7a8dad', 'bytes': 1164202}, 'Script.pvf': {'sha256': '95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c', 'bytes': 126018482}}

BUNDLE008_CHANNEL_BASE = "d701a4d9619b761f91fdd9516fdf8d45a383daba264d947ffd319b8d94ff2748"

BUNDLE008_CHANNEL_SHA = "5630b099ef98e322806c35cec25be7c7aae699ebfbc8f16e60f1a70d12bfe7d7"

BUNDLE008_ID = "POS-20261031-008-guards"

BUNDLE008_INDEXES = {
    'items.json': 'cde6d8d3aa10b370f44c52efc910969ed2bf4de2916e2530eb6da7e59f39d001',
    'pvf-directory.bin': 'f407cd3d89fdb655fddc1650beffa2b315317d9406a4deb910c1cefb8036263a',
    'item-details-v3-95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c.bin': 'bd45b0120600d8064897af3c7a61d23a23e462aa7ebf7138f9780673bcb71062',
    'quests-v1-95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c.bin': '6c5cb0e92d894dc6dd390aa56b8a198abf1fc36aff343b4fe33fc712dd2f828a',
    'professions-v1-95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c.bin': 'ecfd4e0cc2c4fab8e1028b1bdbfb32742c4c61cf45195b850822a39b38c5b485',
    'expert-jobs-v1-95147859374186dbeb99f998829a1c642f949e56942ac98be8b5af4492aefe1c.json': 'feb985cc8f0e7a11dd37a869222fbe50cc281eb7a403360fbc7c53573f1d8dc3',
}

def checked_bundle008_channel(spec):
    """The accepted full table replaces seven obsolete PVP ports and adds one."""
    expected = dict(bundle=BUNDLE008_ID, target=RUNTIME_CHANNEL_TARGET,
        container_path=RUNTIME_CHANNEL_CONTAINER, channel_file=RUNTIME_CHANNEL_FILE,
        base_channel_source='external:approved-119-empire-arena-pvfmap-v2',
        channel_source='external:approved-120-bundle008',
        base_sha256=BUNDLE008_CHANNEL_BASE, sha256=BUNDLE008_CHANNEL_SHA,
        pvf_sha256='da925d370e72e7662e96d05d48f58154a2c53d6a97f17306d962cb593ded0b5f',
        base_channels=119, channels=120, channel_id=75, port=10075,
        channel_type=8, group='pvp', name='<chn_channel_info_038>')
    if not isinstance(spec, dict) or spec != expected:
        raise ValueError('cumulative channel binding differs')
    return spec

def checked_bundle008_channel_bytes(before, after):
    import hashlib
    if hashlib.sha256(before).hexdigest() != BUNDLE008_CHANNEL_BASE or hashlib.sha256(after).hexdigest() != BUNDLE008_CHANNEL_SHA:
        raise ValueError('cumulative channel bytes differ')

def checked_bundle008_ports(before, after):
    retired = set(range(10501, 10508))
    added = {10075, *range(10191, 10198)}
    if (len(before) != 119 or len(set(before)) != 119 or len(after) != 120 or len(set(after)) != 120
            or set(before) - set(after) != retired or set(after) - set(before) != added):
        raise ValueError('cumulative channel port delta differs')

def checked_resource_channel_bundle(pair, channel):
    """The accepted nine-file set and the 120-row table are inseparable."""
    pair_bundle = pair.get('bundle') if isinstance(pair, dict) else None
    channel_bundle = channel.get('bundle') if isinstance(channel, dict) else None
    if BUNDLE008_ID in (pair_bundle, channel_bundle):
        if pair_bundle != BUNDLE008_ID or channel_bundle != BUNDLE008_ID:
            raise ValueError('cumulative resources require their paired channel table')
        checked_bundle008_channel(channel)


def checked_file_records(records):
    """Check precisely nine unique canonical paths, hashes and raw lengths."""
    if not isinstance(records, list) or len(records) != len(BUNDLE008_CLIENT_FILES):
        raise ValueError('bundle 008 requires exactly nine file records')
    seen = set()
    for record in records:
        if not isinstance(record, dict) or set(record) != {'path', 'sha256', 'bytes'}:
            raise ValueError('invalid bundle file record')
        name = record['path']
        if not isinstance(name, str) or name.casefold() in seen or name not in BUNDLE008_CLIENT_FILES:
            raise ValueError('unknown or duplicate bundle file path')
        seen.add(name.casefold())
        expected = BUNDLE008_CLIENT_FILES[name]
        if (type(record['bytes']) is not int or record['bytes'] != expected['bytes']
                or record['sha256'] != expected['sha256']):
            raise ValueError('bundle file identity differs: ' + name)
    return records


def file_digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(block)
    return result.hexdigest()


def inspect_bundle(root, channel, indexes):
    """Read original assets only; never publish, install or change a service."""
    root, indexes = root.resolve(strict=True), indexes.resolve(strict=True)
    records = []
    for name in BUNDLE008_CLIENT_FILES:
        path = root / name
        if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(root):
            raise ValueError('bundle file is missing or linked: ' + name)
        records.append(dict(path=name, sha256=file_digest(path), bytes=path.stat().st_size))
    checked_file_records(records)
    if channel.is_symlink() or file_digest(channel) != BUNDLE008_CHANNEL_SHA:
        raise ValueError('bundle 008 channel identity differs')
    for name, expected in BUNDLE008_INDEXES.items():
        path = indexes / name
        if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(indexes) or file_digest(path) != expected:
            raise ValueError('bundle 008 index differs: ' + name)
    return dict(bundle=BUNDLE008_ID, files=records, channelSha256=BUNDLE008_CHANNEL_SHA,
                channels=120, indexes=dict(BUNDLE008_INDEXES), readOnly=True,
                historicalBaseline=True, deploymentPerformed=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True, type=Path)
    parser.add_argument('--channel', required=True, type=Path)
    parser.add_argument('--indexes', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(inspect_bundle(args.root, args.channel, args.indexes), indent=2))
