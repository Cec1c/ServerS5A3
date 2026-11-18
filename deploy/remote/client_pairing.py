"""Exact reviewed client identities for release and paired-resource admission."""

FEATURE_CLIENT_SHA = '6402835223a23f9e4300c57c175d1b990b78ab0eecc46bd03fd373d0893e73f4'

FEATURE_CLIENT_BYTES = 112812032

FEATURE_CLIENT_FILES = {
    'DNF.exe': {'sha256': FEATURE_CLIENT_SHA, 'bytes': FEATURE_CLIENT_BYTES},
    'DpsMeter.dll': {'sha256': '6e907abd58551c06e58242d31e4a8a62c57d7d2440d88b7184f9844c34be5a17', 'bytes': 493056},
    'CombatPower.dll': {'sha256': '140ff544f282e401801a5d46d067a74867b43a2ecce7d89b5abe065a00a59f77', 'bytes': 706048},
    'ImagePacks2/sprite_interface_private_features20261113.NPK': {
        'sha256': 'fc8d96ccb192e3bdaed211b2bfb81a59ee96daab50e9d69e495766019c56ac42', 'bytes': 125034},
}

BLACKDIAMOND_FORMAL_SHA = '28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'

BLACKDIAMOND_FORMAL_BYTES = 112816128

BLACKDIAMOND_FORMAL_FILES = {**FEATURE_CLIENT_FILES,
    'DNF.exe': {'sha256': BLACKDIAMOND_FORMAL_SHA, 'bytes': BLACKDIAMOND_FORMAL_BYTES}}

FEATURE_CLIENT_VARIANTS = {FEATURE_CLIENT_SHA: FEATURE_CLIENT_FILES,
                           BLACKDIAMOND_FORMAL_SHA: BLACKDIAMOND_FORMAL_FILES}

TAG_BUFF_FORMAL_SHA = '40657c19955dc2f594fc2221ef91044170d86406ba6fd193cdb1839877f28cb0'
WEDDING_FEMALE_SHA = 'f16d3bf48b6b802378fb066eaab96add8132c6406d7941a6d8d60328e47f6d8b'
TAG_BUFF_WEDDING_SHA = '58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4'
for _identity in (TAG_BUFF_FORMAL_SHA, WEDDING_FEMALE_SHA, TAG_BUFF_WEDDING_SHA):
    FEATURE_CLIENT_VARIANTS[_identity] = {**BLACKDIAMOND_FORMAL_FILES,
        'DNF.exe': {'sha256': _identity, 'bytes': BLACKDIAMOND_FORMAL_BYTES}}

ROSCH_CLIENT_SHA = 'a853463e2e6b05b6a0f779a8fa27c0787432331b29c517d331574820d3cf43cf'
FEATURE_CLIENT_VARIANTS[ROSCH_CLIENT_SHA] = {**BLACKDIAMOND_FORMAL_FILES,
    'DNF.exe': {'sha256': ROSCH_CLIENT_SHA, 'bytes': 112820224}}

def checked_client_files(pair):
    """Bounded EXE/NPK plus exact reviewed plugin pair; GM assets stay separate."""
    import re
    pins = pair.get('clientFiles', {})
    if 'clientFiles' not in pair:
        if pair.get('exeSha256') in FEATURE_CLIENT_VARIANTS:
            raise ValueError('feature client requires exact EXE, both plugin DLLs and native skin pins')
        return {}
    if pair.get('kind') not in ('paired', 'exe-only') or 'bundle' in pair or not isinstance(pins, dict) or not 1 <= len(pins) <= 33 or 'DNF.exe' not in pins:
        raise ValueError('invalid paired client file set')
    if pair.get('kind') == 'exe-only' and set(pins) != {'DNF.exe'}:
        raise ValueError('EXE-only cannot change NPK')
    if any(not isinstance(name, str) for name in pins) or len({name.casefold() for name in pins}) != len(pins):
        raise ValueError('duplicate paired client paths')
    required_feature_files = FEATURE_CLIENT_VARIANTS.get(pair.get('exeSha256'))
    feature_client = required_feature_files is not None
    if feature_client and (
            pair.get('kind') != 'paired' or
            any(pins.get(name) != pin for name, pin in required_feature_files.items())):
        raise ValueError('feature client requires exact EXE, both plugin DLLs and native skin pins')
    for name, pin in pins.items():
        plugin = feature_client and name in ('DpsMeter.dll', 'CombatPower.dll')
        if name != 'DNF.exe' and not plugin and not re.fullmatch(r'ImagePacks2/[A-Za-z0-9_-][A-Za-z0-9_.-]{0,159}\.NPK', name):
            raise ValueError('only bounded NPK paths and exact reviewed plugin dependencies are allowed')
        if (not isinstance(pin, dict) or set(pin) != {'sha256', 'bytes'} or
                not isinstance(pin['sha256'], str) or not re.fullmatch('[a-f0-9]{64}', pin['sha256']) or
                type(pin['bytes']) is not int or not 0 < pin['bytes'] <= 512 << 20):
            raise ValueError('invalid paired client file pin')
    if pins['DNF.exe']['sha256'] != pair.get('exeSha256'):
        raise ValueError('paired EXE pin differs from profile')
    return pins
