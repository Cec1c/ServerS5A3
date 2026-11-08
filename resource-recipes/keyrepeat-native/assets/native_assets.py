# -*- coding: utf-8 -*-
"""Read-only: list sprite_interface2_hud_information_button.NPK and decode
sprite/interface2/hud/information_button.img frame index.

Only reads the NPK; writes contact sheets / JSON into the session directory.
"""
import os, io, sys, json, struct, zlib, math, hashlib

from PIL import Image, ImageDraw, ImageFont

IMG_MAGIC = b'Neople Img File\0'
LINK = 0x11


def npk_key():
    key = bytearray(256)
    h = b'puchikon@neople dungeon and fighter '
    key[:len(h)] = h
    for i in range(len(h), 255):
        key[i] = b'DNF'[i % 3]
    return bytes(key)


KEY = npk_key()


def read_index(p):
    out = []
    with open(p, 'rb') as f:
        magic = f.read(16)
        if magic != b'NeoplePack_Bill\0':
            raise ValueError('not npk: %r' % magic)
        n = struct.unpack('<i', f.read(4))[0]
        for _ in range(n):
            off = struct.unpack('<i', f.read(4))[0]
            ln = struct.unpack('<i', f.read(4))[0]
            enc = f.read(256)
            dec = bytes(v ^ KEY[i] for i, v in enumerate(enc))
            out.append((off, ln, dec.split(b'\0', 1)[0].decode('utf-8', 'replace')))
    return out


def decode_pixels(data, w, h, cb):
    n = w * h
    rgba = bytearray(n * 4)
    if cb == 0x10:
        rgba = bytearray(data[:n * 4])
        for i in range(n):
            r, g, b, a = rgba[i * 4:i * 4 + 4]
            rgba[i * 4:i * 4 + 4] = bytes((b, g, r, a))
    elif cb in (0x0E, 0x0F):
        for i in range(n):
            v = struct.unpack_from('<H', data, i * 2)[0]
            if cb == 0x0E:
                a = 255 if v & 0x8000 else 0
                r = ((v >> 10) & 0x1F) * 255 // 31
                g = ((v >> 5) & 0x1F) * 255 // 31
                b = (v & 0x1F) * 255 // 31
            else:
                a = ((v >> 12) & 0x0F) * 17
                r = ((v >> 8) & 0x0F) * 17
                g = ((v >> 4) & 0x0F) * 17
                b = (v & 0x0F) * 17
            rgba[i * 4:i * 4 + 4] = bytes((r, g, b, a))
    else:
        raise ValueError('color bits 0x%02X unsupported' % cb)
    return Image.frombytes('RGBA', (w, h), bytes(rgba))


def ri32(pl):
    return struct.unpack('<i', pl.read(4))[0]


def ri64(pl):
    return struct.unpack('<q', pl.read(8))[0]


def read_img(p, entry):
    with open(p, 'rb') as f:
        f.seek(entry[0])
        data = f.read(entry[1])
    pl = io.BytesIO(data)
    if pl.read(16) != IMG_MAGIC:
        return None, None
    idx_len = ri64(pl)
    ver = ri32(pl)
    spr_n = ri32(pl)
    if ver == 2:
        sprites = []
        direct = []
        for i in range(spr_n):
            cb = ri32(pl)
            s = {'i': i, 'cb': cb}
            sprites.append(s)
            if cb == LINK:
                s['target'] = ri32(pl)
                continue
            s['cm'] = ri32(pl); s['w'] = ri32(pl); s['h'] = ri32(pl); s['len'] = ri32(pl)
            s['x'] = ri32(pl); s['y'] = ri32(pl); s['fw'] = ri32(pl); s['fh'] = ri32(pl)
            if s['cm'] == 0x05:
                bpp = 4 if cb == 0x10 else 2
                s['len'] = s['w'] * s['h'] * bpp
            direct.append(s)
        imgs = [None] * spr_n
        for s in direct:
            s['data'] = pl.read(s['len'])
            raw = zlib.decompress(s['data']) if s['cm'] == 0x06 else s['data']
            imgs[s['i']] = decode_pixels(raw, s['w'], s['h'], s['cb'])
        for s in sprites:
            if s.get('target') is not None:
                imgs[s['i']] = imgs[s['target']]
        return {'version': 2, 'sprite_count': spr_n, 'bytes': len(data)}, sprites, imgs
    if ver != 5:
        return {'version': ver, 'sprite_count': spr_n, 'bytes': len(data), 'unsupported': True}, None, None
    tex_n = ri32(pl); album = ri32(pl); pal = ri32(pl)
    if pal:
        pl.seek(pal * 4, io.SEEK_CUR)
    textures = []
    for k in range(tex_n):
        textures.append({'v': ri32(pl), 'cb': ri32(pl), 'idx': ri32(pl), 'len': ri32(pl),
                         'full': ri32(pl), 'w': ri32(pl), 'h': ri32(pl)})
    sprites = []
    direct = []
    for i in range(spr_n):
        cb = ri32(pl)
        s = {'i': i, 'cb': cb}
        sprites.append(s)
        if cb == LINK:
            s['target'] = ri32(pl)
            continue
        s['cm'] = ri32(pl); s['w'] = ri32(pl); s['h'] = ri32(pl); s['len'] = ri32(pl)
        s['x'] = ri32(pl); s['y'] = ri32(pl); s['fw'] = ri32(pl); s['fh'] = ri32(pl)
        if cb < LINK and s['len']:
            direct.append(s)
            continue
        s['unknown'] = ri32(pl); s['tex'] = ri32(pl)
        s['left'] = ri32(pl); s['top'] = ri32(pl); s['right'] = ri32(pl); s['bottom'] = ri32(pl); s['rot'] = ri32(pl)
    for t in textures:
        t['data'] = pl.read(t['len'])
    for s in direct:
        s['data'] = pl.read(s['len'])
    timgs = {}
    for t in textures:
        raw = zlib.decompress(t['data']) if t['len'] != t['full'] else t['data']
        if t['cb'] >= LINK:
            raise SystemExit('DDS/DXT atlas 0x%02X unsupported' % t['cb'])
        timgs[t['idx']] = decode_pixels(raw, t['w'], t['h'], t['cb'])
    imgs = [None] * spr_n
    for s in sprites:
        if s.get('target') is not None:
            continue
        if s['cb'] < LINK and s['len']:
            raw = zlib.decompress(s['data']) if s['cm'] in (0x06, 0x07) else s['data']
            imgs[s['i']] = decode_pixels(raw, s['w'], s['h'], s['cb'])
            continue
        atlas = timgs[s['tex']]
        im = atlas.crop((s['left'], s['top'], s['right'], s['bottom']))
        if s['rot']:
            im = im.transpose(Image.Transpose.TRANSPOSE)
        imgs[s['i']] = im
    for s in sprites:
        if s.get('target') is not None:
            imgs[s['i']] = imgs[s['target']]
    return {'version': 5, 'sprite_count': spr_n, 'textures': tex_n, 'bytes': len(data)}, sprites, imgs


def sheet(imgs, path, cols=12, cw=64, chh=76, label_off=0):
    n = len(imgs)
    rws = max(1, math.ceil(n / cols))
    sh = Image.new('RGB', (cols * cw, rws * chh), (28, 31, 38))
    d = ImageDraw.Draw(sh)
    font = ImageFont.load_default()
    for i, im in enumerate(imgs):
        x = (i % cols) * cw
        y = (i // cols) * chh
        bgc = Image.new('RGBA', im.size, (56, 60, 70, 255))
        prev = Image.alpha_composite(bgc, im)
        prev.thumbnail((cw - 6, chh - 22), Image.Resampling.NEAREST)
        sh.paste(prev.convert('RGB'), (x + 3, y + 18))
        d.text((x + 3, y + 4), '#%d %dx%d' % (i + label_off, im.width, im.height), fill=(246, 222, 150), font=font)
    sh.save(path)
    return sh.size

