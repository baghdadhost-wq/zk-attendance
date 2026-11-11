#!/bin/sh
# يبني برنامج الحاسبة والسيرفر لكل الأنظمة. يحتاج Go 1.24 أو أحدث (https://go.dev)
# تطبيق الآيفون ينبني على GitHub Actions (.github/workflows/ios.yml) لأنه يحتاج ماك.
set -e
cd "$(dirname "$0")"
build() { mkdir -p dist/$3; CGO_ENABLED=0 GOOS=$1 GOARCH=$2 GOARM=7 go build -trimpath -ldflags "-s -w" -o dist/$3/$4 ./cmd/zk-attendance; echo "dist/$3/$4"; }
build windows amd64 windows zk-attendance.exe
build linux   amd64 linux-x64 zk-attendance
build linux   arm64 linux-arm64 zk-attendance
build linux   arm   linux-arm32 zk-attendance
build darwin  arm64 mac-apple-silicon zk-attendance
build darwin  amd64 mac-intel zk-attendance
build android arm64 android-arm64 zk-attendance
