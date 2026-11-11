#!/bin/sh
# يجهز النسخة العامة للتوزيع لكل الأجهزة بملفات مضغوطة بمجلد dist:
#   ويندوز، لينكس (x64 و arm64 و arm32)، ماك (Intel و Apple Silicon)، أندرويد (arm64 يشتغل بـ Termux).
# تطبيق الآيفون والآيباد ينبني على GitHub Actions (.github/workflows/ios.yml) عند وسم إصدار v*.
#
# التعليمات: README كل منصة = tools/pkg/head-<منصة>.txt + common.txt + tail-<منصة>.txt
# (القسم المشترك بملف واحد حتى ما تختلف التعليمات بين المنصات)
set -e
cd "$(dirname "$0")/.."
V=$(sed -n 's/^const Version = "\(.*\)"/\1/p' server/server.go)
[ -n "$V" ] || { echo "ما لكيت رقم الإصدار بـ server/server.go" >&2; exit 1; }
rm -rf dist/pkg && mkdir -p dist/pkg

build() { # GOOS GOARCH OUT [خيارات go build إضافية]
	os=$1 arch=$2 out=$3
	shift 3
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=6 go build -trimpath -ldflags "-s -w" "$@" -o "$out" ./cmd/zk-attendance
}

esc() { printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'; } # للاستبدال الآمن بـ sed

for f in common head-windows tail-windows head-linux tail-linux head-mac tail-mac head-android tail-android; do
	[ -f "tools/pkg/$f.txt" ] || { echo "ناقص tools/pkg/$f.txt" >&2; exit 1; }
done

readme() { # PLATFORM SEP RESET  -> يطبع README المنصة
	cat "tools/pkg/head-$1.txt" tools/pkg/common.txt "tools/pkg/tail-$1.txt" |
		sed -e "s|@VERSION@|$(esc "$V")|g" -e "s|@SEP@|$(esc "$2")|g" -e "s|@RESET@|$(esc "$3")|g"
}

# linux-arm32 بـ GOARM=6 حتى يشتغل على Raspberry Pi Zero و 1 (armv6) وكذلك armv7

# ويندوز: نهايات أسطر CRLF و BOM حتى Notepad القديم يعرض العربي صح
W=dist/pkg/zk-attendance-windows
mkdir -p $W
build windows amd64 $W/zk-attendance.exe
printf '@echo off\r\ncd /d "%%~dp0"\r\nzk-attendance.exe --network\r\npause\r\n' >$W/start-network.bat
printf '@echo off\r\ncd /d "%%~dp0"\r\nzk-attendance.exe --demo\r\npause\r\n' >$W/start-demo.bat
printf '@echo off\r\ncd /d "%%~dp0"\r\nzk-attendance.exe --reset-admin\r\npause\r\n' >$W/reset-admin.bat
readme windows '\' 'شغّل reset-admin.bat' | sed 's/$/\r/' | { printf '\357\273\277'; cat; } >$W/README.txt

L=dist/pkg/zk-attendance-linux
mkdir -p $L/linux-x64 $L/linux-arm64 $L/linux-arm32
build linux amd64 $L/linux-x64/zk-attendance
build linux arm64 $L/linux-arm64/zk-attendance
build linux arm $L/linux-arm32/zk-attendance
cp tools/pkg/zk-attendance.service $L/
readme linux / 'شغّل: cd /opt/zk-attendance && sudo ./zk-attendance --reset-admin' >$L/README.txt

M=dist/pkg/zk-attendance-mac
mkdir -p $M/mac-intel $M/mac-apple-silicon
build darwin amd64 $M/mac-intel/zk-attendance
build darwin arm64 $M/mac-apple-silicon/zk-attendance
readme mac / 'بـ Terminal: cd ~/zk-attendance && ./zk-attendance --reset-admin' >$M/README.txt

# أندرويد يحتاج ملف PIE
A=dist/pkg/zk-attendance-android
mkdir -p $A
build android arm64 $A/zk-attendance -buildmode=pie
readme android / 'بـ Termux: ./zk-attendance --reset-admin' >$A/README.txt

chmod 755 $L/*/zk-attendance $M/*/zk-attendance $A/zk-attendance
if grep -l '@[A-Z][A-Z]*@' $W/README.txt $L/README.txt $M/README.txt $A/README.txt; then
	echo "بقى متغير ما انستبدل بالتعليمات" >&2
	exit 1
fi

cd dist/pkg
for p in windows linux mac android; do
	rm -f ../zk-attendance-$p.zip
	zip -qr -X ../zk-attendance-$p.zip zk-attendance-$p
done
echo "نسخة $V:"
ls -l ../zk-attendance-*.zip
