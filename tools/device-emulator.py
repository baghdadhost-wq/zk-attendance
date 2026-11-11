"""ZKTeco TCP device emulator for testing (port 4370), closer to real devices.
Env: EMU_NAMES=utf8|cp1256|ascii  EMU_LOGS=N  EMU_REC=40|16|8  EMU_COMMKEY=n  EMU_MISMATCH=1  EMU_IGNORE_WRITE=1
Logs: CMD <code> <hex> | BADCHK | USERWRQ <hex> | SETTIME <iso>
"""
import os, socket, struct, threading, datetime, sys, random, time
LOG = open(sys.argv[1] if len(sys.argv) > 1 else '/tmp/emu.log', 'a', buffering=1)
MAGIC = b'\x50\x50\x82\x7d'
MODE = os.environ.get('EMU_NAMES', 'utf8')
NLOGS = int(os.environ.get('EMU_LOGS', '25'))
REC = int(os.environ.get('EMU_REC', '40'))
COMMKEY = os.environ.get('EMU_COMMKEY')
SESSION = 0x1234

def nm(t, fb):
    if MODE == 'ascii': return fb
    return t.encode('cp1256' if MODE == 'cp1256' else 'utf-8')
def enc_time(d):
    return (((d.year % 100) * 12 * 31 + (d.month - 1) * 31 + d.day - 1) * 86400 + (d.hour * 60 + d.minute) * 60 + d.second)
def dec_time(t):
    s = t % 60; t //= 60; mi = t % 60; t //= 60; h = t % 24; t //= 24
    d = t % 31 + 1; t //= 31; mo = t % 12 + 1; t //= 12
    return datetime.datetime(t + 2000, mo, d, h, mi, s)
def user_rec(uid, user_id, name, pw=b'', role=0, card=0, group=b'1'):
    return struct.pack('<HB8s24sIx7sx24s', uid, role, pw, name, card, group, user_id.encode())
users = [(1, '1', nm('أحمد علي', b'Ahmed'), b'1234', 14, 555, b'1'),
         (2, '2', nm('زينب كاظم', b'Zainab'), b'', 0, 0, b'1'),
         (3, '3', nm('محمد', b'Mohammed'), b'', 0, 0, b'1'),
         (4, '4', b'John', b'', 0, 0, b'1')]
RAW = {u[0]: user_rec(*u) for u in users}
base = datetime.datetime(2026, 10, 1, 8, 0, 0)
logs = [(str((i % 4) + 1), base + datetime.timedelta(minutes=i * 37)) for i in range(NLOGS)]
state = {'time_offset': -125}
OPTIONS = {'~SerialNumber': 'EMU0001', '~Platform': 'ZMM220_TFT', '~DeviceName': 'iFace1000', 'MAC': '00:17:61:aa:bb:cc',
           'ZKFaceVersion': '7', '~ZKFPVersion': '10', 'IPAddress': '127.0.0.1', 'NetMask': '255.255.255.0', 'GATEIPAddress': '192.168.1.1'}

def checksum(p):  # pyzk __create_checksum
    l = len(p); c = 0; i = 0
    while l > 1:
        c += p[i] | (p[i+1] << 8); i += 2
        if c > 65535: c -= 65535
        l -= 2
    if l: c += p[-1]
    while c > 65535: c -= 65535
    c = ~c
    while c < 0: c += 65535
    return c
def make_commkey(key, session_id, ticks=50):  # pyzk make_commkey
    key = int(key); k = 0
    for i in range(32):
        k = (k << 1 | 1) if (key & (1 << i)) else (k << 1)
    k += session_id
    k = struct.unpack('BBBB', struct.pack('I', k))
    k = struct.pack('BBBB', k[0] ^ ord('Z'), k[1] ^ ord('K'), k[2] ^ ord('S'), k[3] ^ ord('O'))
    k = struct.unpack('HH', k); k = struct.pack('HH', k[1], k[0])
    B = 0xff & ticks; k = struct.unpack('BBBB', k)
    return struct.pack('BBBB', k[0] ^ B, k[1] ^ B, B, k[3] ^ B)

def frame(cmd, rid, data=b''):
    payload = struct.pack('<4H', cmd, 0, SESSION, rid) + data
    return MAGIC + struct.pack('<I', len(payload)) + payload

def user_buf():
    body = b''.join(RAW[k] for k in sorted(RAW))
    return struct.pack('<I', len(body)) + body
def log_buf():
    uid_of = {u[1]: u[0] for u in users}
    out = []
    for uid, t in logs:
        ts = struct.pack('<I', enc_time(t))
        if REC == 8: out.append(struct.pack('<HB4sB', uid_of.get(uid, 0), 1, ts, 0))
        elif REC == 16: out.append(struct.pack('<I4sBB2sI', int(uid), ts, 1, 0, b'', 0))
        else: out.append(struct.pack('<H24sB4sB8s', 1, uid.encode(), 1, ts, 0, b''))
    body = b''.join(out)
    return struct.pack('<I', len(body)) + body

def send_chunked(conn, data):  # simulate TCP segmentation
    i = 0
    while i < len(data):
        n = random.randint(1, 3000)
        conn.sendall(data[i:i+n]); i += n
        if random.random() < 0.05: time.sleep(0.002)

def client(conn):
    buf = b''; authed = COMMKEY is None; pending = {'buf': None}
    while True:
        chunk = conn.recv(65536)
        if not chunk: break
        buf += chunk
        while len(buf) >= 8 and buf[:4] == MAGIC:
            ln = struct.unpack('<I', buf[4:8])[0]
            if len(buf) < 8 + ln: break
            p = buf[8:8+ln]; buf = buf[8+ln:]
            cmd, chk, sess, rid = struct.unpack('<4H', p[:8]); data = p[8:]
            # pyzk computes the checksum with the previous reply id
            prev = rid - 1 if rid > 0 else 65534
            if checksum(struct.pack('<4H', cmd, 0, sess, prev) + data) != chk: LOG.write(f'BADCHK cmd={cmd}\n')
            LOG.write(f'CMD {cmd} {data.hex()[:120]}\n')
            ok = lambda d=b'': frame(2000, rid, d)
            if cmd == 1000:
                conn.sendall(frame(2005 if COMMKEY else 2000, rid)); continue
            if cmd == 1102:
                good = data == make_commkey(int(COMMKEY), SESSION)
                LOG.write(f'AUTH {"ok" if good else "bad"}\n'); authed = authed or good
                conn.sendall(frame(2000 if good else 2005, rid)); continue
            if not authed:
                conn.sendall(frame(2005, rid)); continue
            if cmd in (1001, 1002, 1003, 1013, 1017, 31, 66, 67, 1004): conn.sendall(ok()); continue
            if cmd == 1005: continue
            if cmd == 1100: conn.sendall(ok(b'Ver 8.0.4.3-20190506\x00')); continue
            if cmd == 11:
                key = data.split(b'\x00')[0].decode()
                conn.sendall(ok(f'{key}={OPTIONS[key]}\x00'.encode()) if key in OPTIONS else frame(2001, rid)); continue
            if cmd == 69: conn.sendall(ok(bytes([9, 0]))); continue
            if cmd == 50:
                n = len(logs) + (3 if os.environ.get('EMU_MISMATCH') else 0)
                f = [0] * 20
                f[4], f[6], f[8], f[12] = len(RAW), 7, n, 0
                f[14], f[15], f[16], f[17], f[18], f[19] = 4000, 10000, 100000, 3993, 9996, 100000 - n
                conn.sendall(ok(struct.pack('<20i', *f) + struct.pack('<3i', 4, 0, 3000))); continue
            if cmd == 201:
                t = datetime.datetime.now() + datetime.timedelta(seconds=state['time_offset'])
                conn.sendall(ok(struct.pack('<I', enc_time(t)))); continue
            if cmd == 202:
                t = dec_time(struct.unpack('<I', data[:4])[0])
                state['time_offset'] = (t - datetime.datetime.now()).total_seconds()
                LOG.write(f'SETTIME {t.isoformat()}\n'); conn.sendall(ok()); continue
            if cmd == 15: logs.clear(); conn.sendall(ok()); continue
            if cmd == 8:
                LOG.write(f'USERWRQ {data.hex()}\n')
                if not os.environ.get('EMU_IGNORE_WRITE') and len(data) == 72:
                    RAW[struct.unpack('<H', data[:2])[0]] = bytes(data)
                conn.sendall(ok()); continue
            if cmd == 1503:
                b = user_buf() if data[1] == 0x09 else log_buf()
                if len(b) <= 1024:  # small: data directly
                    send_chunked(conn, frame(1501, rid, b)); continue
                pending['buf'] = b   # large: client reads it in chunks
                conn.sendall(ok(b'\x00' + struct.pack('<I', len(b)) + b'\x00' * 4)); continue
            if cmd == 1504:
                start, size = struct.unpack('<ii', data[:8]); part = pending['buf'][start:start+size]
                out = frame(1500, rid, struct.pack('<I', len(part)) + b'\x00' * 4)
                for i in range(0, len(part), 9000):  # several DATA frames per chunk
                    out += frame(1501, rid, part[i:i+9000])
                out += frame(2000, rid)
                send_chunked(conn, out); continue
            if cmd == 1502: pending['buf'] = None; conn.sendall(ok()); continue
            conn.sendall(frame(2001, rid))
    conn.close()

s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', 4370)); s.listen(5)
while True:
    c, _ = s.accept(); threading.Thread(target=client, args=(c,), daemon=True).start()
