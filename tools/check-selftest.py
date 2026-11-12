"""يتأكد من نتائج الاختبار التلقائي للتطبيق على المحاكي."""
import json, os, re, sys
from datetime import datetime

ok = True
for path in sys.argv[1:]:
    r = json.load(open(path))
    checks = {
        'login required (first open asks to create the account)': r.get('login') == 'setup',
        'serves the network like the PC (members link shown)': r.get('shareMode') == 'phone' and bool(r.get('shareUrls')),
        'pulled data from the device': r.get('fetch', '').startswith('تم السحب'),
        'device info shown': r.get('infoRows', 0) >= 10,
        'confirm dialog handled': 'متأكد' in r.get('confirm', ''),
        'disable + enable commands': 'تم إيقاف' in r.get('disable', '') and 'تم تفعيل' in r.get('enable', ''),
        'report rows': r.get('summaryRows', 0) >= 1,
        'excel export reached the share sheet': r.get('download', {}).get('bytes', 0) > 0,
        'no script error': 'error' not in r,
    }
    net_file = path.replace('-selftest.json', '-net.json')
    net = json.load(open(net_file)) if os.path.exists(net_file) else {}
    checks['another device opens the phone link'] = any(v.get('status') == 200 for v in net.values())
    m = re.search(r'(\d\d):(\d\d):\d\d', r.get('jsTime', ''))
    g = r.get('goTime', '')
    if m and len(g) >= 16:
        js_min = int(m.group(1)) * 60 + int(m.group(2))
        go_min = int(g[11:13]) * 60 + int(g[14:16])
        checks['program time matches phone time zone'] = abs(js_min - go_min) <= 2 or abs(js_min - go_min) >= 1438
    print(f'\n{path}  (zone: {r.get("jsZone")}, program time {g}, phone time {r.get("jsTime", "")[:24]}, pull took {r.get("fetchMs")} ms)')
    if r.get('jsZone') != 'Asia/Baghdad':
        print('  NOTE  simulator is not on Baghdad time, so the time-zone check is weaker')
    for name, passed in checks.items():
        print(('  PASS  ' if passed else '  FAIL  ') + name)
        ok &= passed
sys.exit(0 if ok else 1)
