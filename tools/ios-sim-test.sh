#!/bin/bash
# يجرب التطبيق على محاكي آيفون وآيباد: يتصل بمحاكي جهاز البصمة، يسحب البيانات، ينفذ أوامر، ويصدّر ملف
set -uo pipefail
APP=build/sim/Build/Products/Debug-iphonesimulator/ZKAttendance.app
BID=com.baghdadhost.zkattendance
OUT=out/test
mkdir -p "$OUT"

TZ=Asia/Baghdad EMU_LOGS=800 python3 tools/device-emulator.py "$OUT/emulator.log" > "$OUT/emulator.out" 2>&1 &
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

shot() {
  xcrun simctl io "$1" screenshot "$OUT/tmp.png" > /dev/null 2>&1 &&
    sips -s format jpeg -s formatOptions 70 -Z 1400 "$OUT/tmp.png" --out "$OUT/$2.jpg" > /dev/null 2>&1
  rm -f "$OUT/tmp.png"
}

run_on() {
  local kind=$1 udid data="" result=""
  udid=$(pick "$kind")
  echo "== $kind: $udid"
  xcrun simctl boot "$udid" || true
  xcrun simctl bootstatus "$udid" -b > /dev/null
  echo "booted, letting the simulator settle"
  sleep 30
  xcrun simctl install "$udid" "$APP"
  xcrun simctl launch "$udid" "$BID" -zkSelfTest 127.0.0.1
  for i in $(seq 1 30); do
    sleep 4
    data=$(xcrun simctl get_app_container "$udid" "$BID" data)
    [ $((i % 3)) -eq 1 ] && shot "$udid" "$kind-$(printf %02d "$i")"
    if grep -q '"done"' "$data/Documents/selftest.json" 2> /dev/null; then result=done; break; fi
  done
  shot "$udid" "$kind-report"
  sleep 5
  shot "$udid" "$kind-share"
  cp "$data/Documents/selftest.json" "$OUT/$kind-selftest.json"
  echo "--- $kind:"; cat "$OUT/$kind-selftest.json"
  xcrun simctl shutdown "$udid" || true
  [ "$result" = done ] || echo "WARNING: $kind self-test did not finish"
}

run_on iPhone
run_on iPad
python3 tools/check-selftest.py "$OUT/iPhone-selftest.json" "$OUT/iPad-selftest.json"
