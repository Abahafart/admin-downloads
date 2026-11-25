#!/usr/bin/env python3
"""Independent reference implementation (python 'cryptography') used to
generate MEGA known-answer vectors and the folder fixture for the Go tests."""
import base64, hashlib, json, struct, sys, os
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

out = sys.argv[1]

def b64(b): return base64.urlsafe_b64encode(b).rstrip(b'=').decode()
def a32(b): return list(struct.unpack('>%dI' % (len(b)//4), b))
def tob(a): return struct.pack('>%dI' % len(a), *a)
def ecb_enc(k, d):
    e = Cipher(algorithms.AES(k), modes.ECB()).encryptor(); return e.update(d) + e.finalize()
def cbc_enc(k, iv, d):
    e = Cipher(algorithms.AES(k), modes.CBC(iv)).encryptor(); return e.update(d) + e.finalize()
def ctr(k, iv, d):
    e = Cipher(algorithms.AES(k), modes.CTR(iv)).encryptor(); return e.update(d) + e.finalize()
def pad16(b): return b + b'\0' * (-len(b) % 16)

def plaintext(n):
    out = bytearray(); i = 0
    while len(out) < n:
        out += hashlib.sha256(struct.pack('>Q', i)).digest(); i += 1
    return bytes(out[:n])

def chunks(size):
    p = 0; i = 1; res = []
    while size > 0:
        c = i * 131072 if i <= 8 else 1048576
        c = min(c, size); res.append((p, c)); p += c; size -= c; i += 1
    return res

def meta_mac(key, nonce, data):
    mac = bytes(16)
    for (p, c) in chunks(len(data)):
        cm = cbc_enc(key, nonce + nonce, pad16(data[p:p+c]))[-16:]
        mac = ecb_enc(key, bytes(x ^ y for x, y in zip(mac, cm)))
    m = a32(mac)
    return tob([m[0] ^ m[1], m[2] ^ m[3]])

def file_key(aes, nonce, mm):
    a = a32(aes); n = a32(nonce); m = a32(mm)
    return tob([a[0]^n[0], a[1]^n[1], a[2]^m[0], a[3]^m[1], n[0], n[1], m[0], m[1]])

def enc_attr(key, attr_json):
    return b64(cbc_enc(key, bytes(16), pad16(b'MEGA' + attr_json.encode())))

def prepare_key(arr):
    pkey = tob([0x93C467E3, 0x7DB0C7A4, 0xD1BE3F81, 0x0152CB56])
    encs = []
    for j in range(0, len(arr), 4):
        key = [arr[j+i] if j+i < len(arr) else 0 for i in range(4)]
        encs.append(Cipher(algorithms.AES(tob(key)), modes.ECB()).encryptor())
    for _ in range(0x10000):
        for e in encs:
            pkey = e.update(pkey)
    return pkey

def stringhash(s, key):
    s32 = a32(pad4(s.encode())); h = [0, 0, 0, 0]
    for i, v in enumerate(s32): h[i & 3] ^= v
    hb = tob(h); e = Cipher(algorithms.AES(key), modes.ECB()).encryptor()
    for _ in range(16384): hb = e.update(hb)
    h = a32(hb)
    return b64(tob([h[0], h[2]]))

def pad4(b): return b + b'\0' * (-len(b) % 4)

vec = {"files": []}
for idx, size in enumerate([10, 131072, 131073, 1572987]):
    aes = hashlib.sha256(b'aes%d' % idx).digest()[:16]
    nonce = hashlib.sha256(b'nonce%d' % idx).digest()[:8]
    pt = plaintext(size)
    ct = ctr(aes, nonce + bytes(8), pt)
    mm = meta_mac(aes, nonce, pt)
    fk = file_key(aes, nonce, mm)
    vec["files"].append({
        "size": size, "link_key": b64(fk), "aes_key_hex": aes.hex(), "nonce_hex": nonce.hex(),
        "meta_mac_hex": mm.hex(), "ciphertext_sha256": hashlib.sha256(ct).hexdigest(),
        "attr": enc_attr(aes, '{"n":"vector %d.bin","c":"x"}' % idx), "name": "vector %d.bin" % idx,
    })

pw = "correct horse battery"
email = "Tester@Example.com"
pk = prepare_key(a32(pad4(pw.encode())))
salt = hashlib.sha256(b'salt').digest()
dk = hashlib.pbkdf2_hmac('sha512', pw.encode(), salt, 100000, 32)
vec["login"] = {"password": pw, "email": email, "v1_passkey_hex": pk.hex(),
    "v1_uh": stringhash(email.lower(), pk), "v2_salt": b64(salt),
    "v2_passkey_hex": dk[:16].hex(), "v2_uh": b64(dk[16:])}

# Folder fixture.
fkey = hashlib.sha256(b'folderkey').digest()[:16]
def h(name): return b64(hashlib.sha256(name.encode()).digest()[:6])
R = h('root')
nodes = []
def folder(handle, parent, name):
    k = hashlib.sha256(b'k' + handle.encode()).digest()[:16]
    nodes.append({"h": handle, "p": parent, "u": "OwnerUsr01", "t": 1,
        "a": enc_attr(k, json.dumps({"n": name}, ensure_ascii=False)),
        "k": "%s:%s" % (R, b64(ecb_enc(fkey, k))), "ts": 1700000000})
def file(handle, parent, name, size, attr=None):
    aes = hashlib.sha256(b'a' + handle.encode()).digest()[:16]
    nonce = hashlib.sha256(b'n' + handle.encode()).digest()[:8]
    mm = hashlib.sha256(b'm' + handle.encode()).digest()[:8]
    fk = file_key(aes, nonce, mm)
    a = attr if attr is not None else enc_attr(aes, json.dumps({"n": name}, ensure_ascii=False))
    nodes.append({"h": handle, "p": parent, "u": "OwnerUsr01", "t": 0, "a": a,
        "k": "%s:%s" % (R, b64(ecb_enc(fkey, fk))), "s": size, "ts": 1700000001})
    return b64(fk)
S1, S2, S3 = h('s1'), h('s2'), h('s3')
folder(R, "OwnerRt0", "Mi Carpeta")
folder(S1, R, "Temporada 1")
keys = {}
keys["ep01"] = file(h('f1'), S1, "ep01.mkv", 1000)
file(h('f2'), S1, "ep02.mkv", 2000)
folder(S2, S1, "Extras")
file(h('f3'), S2, "making of.mp4", 3000)
file(h('f4'), R, "readme.txt", 4)
file(h('f5'), R, "../../etc/passwd", 5)
file(h('f6'), R, "", 6, attr="AAAAAAAAAAAAAAAAAAAAAA")
folder(S3, R, "Vacía")
file(h('f7'), "ZZZZZZZZ", "orphan.bin", 7)
fixture = [{"f": nodes, "ok": [], "s": [], "noc": 1}]
with open(os.path.join(out, "folder_listing.json"), "w") as fh:
    json.dump(fixture, fh, indent=1, ensure_ascii=False)
vec["folder"] = {"handle": R, "key": b64(fkey), "sub_handle": S1, "file_handle": h('f1'),
    "ep01_key": keys["ep01"], "bad_attr_handle": h('f6'), "orphan_handle": h('f7')}
with open(os.path.join(out, "vectors.json"), "w") as fh:
    json.dump(vec, fh, indent=1)
print("ok")
