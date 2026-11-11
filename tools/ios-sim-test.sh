#!/bin/bash
# يجرب التطبيق على محاكي آيفون وآيباد: يتصل بمحاكي جهاز البصمة، يسحب البيانات، ينفذ أوامر، ويصدّر ملف
set -euo pipefail
APP=build/sim/Build/Products/Debug-iphonesimulator/ZKAttendance.app
BID=com.baghdadhost.zkattendance
OUT=out/test
mkdir -p "$OUT"

EMU_LOGS=800 python3 tools/device-emulator.py "$OUT/emulator.log" > "$OUT/emulator.out" 2>&1 &
sleep 1

pick() {
  xcrun simctl list devices available -j | python3 -c "
import json, sys
d = json.load(sys.stdin)['devices']
for rt in sorted(d, reverse=True):
    if 'iOS' not in rt: continue
    for x in d[rt]:
        if x['name'].startswith('$1'):
            print(x['udid']); sys.exit()
"
}

run_on() {
  local kind=$1 udid data result=""
  udid=$(pick "$kind")
  echo "== $kind: $udid"
  xcrun simctl boot "$udid" || true
  xcrun simctl bootstatus "$udid" -b
  xcrun simctl install "$udid" "$APP"
  SIMCTL_CHILD_TZ=Asia/Baghdad xcrun simctl launch "$udid" "$BID" -zkSelfTest 127.0.0.1
  for i in $(seq 1 24); do
    sleep 5
    data=$(xcrun simctl get_app_container "$udid" "$BID" data)
    xcrun simctl io "$udid" screenshot "$OUT/$kind-$(printf %02d "$i").png" > /dev/null 2>&1 || true
    if grep -q '"done"' "$data/Documents/selftest.json" 2> /dev/null; then result=done; break; fi
  done
  sleep 5
  xcrun simctl io "$udid" screenshot "$OUT/$kind-final.png"
  cp "$data/Documents/selftest.json" "$OUT/$kind-selftest.json" || true
  echo "--- $kind:"; cat "$OUT/$kind-selftest.json" || true
  xcrun simctl shutdown "$udid" || true
  [ "$result" = done ]
}

run_on iPhone
run_on iPad
python3 tools/check-selftest.py "$OUT/iPhone-selftest.json" "$OUT/iPad-selftest.json"
