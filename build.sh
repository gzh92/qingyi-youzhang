#!/bin/bash
# 情谊有账 一键打包脚本
# 用法: ./build.sh x86|arm
# 功能: 同步前端 → 校验配置 → 编译后端 → 组装 app.tgz → 打 .fpk → 结构自检
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
VERSION="1.0.7"
APPNAME="qingyi-youzhang"

arch="${1:-x86}"
case "$arch" in
  x86) GOARCH="amd64"; SRC="$ROOT/x86"; BUILD="$ROOT/build-x86"; ;;
  arm) GOARCH="arm64"; SRC="$ROOT/arm"; BUILD="$ROOT/build-arm"; ;;
  *)   echo "用法: $0 x86|arm"; exit 1 ;;
esac

if [ ! -d "$SRC/app/server" ]; then
  echo "[错误] 未找到源码目录 $SRC"; exit 1
fi

echo "== 0/5 前端源码同步（单一源，避免两份漂移） =="
if [ -f "$ROOT/情谊有账.html" ]; then
  cp "$ROOT/情谊有账.html" "$SRC/app/www/index.html"
  echo "  已同步 情谊有账.html → $SRC/app/www/index.html"
fi

echo "== 1/5 校验 manifest 必需字段 =="
required=(appname version display_name desc source platform maintainer os_min_version)
missing=0
for f in "${required[@]}"; do
  if ! grep -qE "^${f}=" "$SRC/manifest"; then
    echo "  [错误] manifest 缺少必需字段: $f"; missing=1
  fi
done
[ "$missing" = "0" ] || exit 1
want_platform="$arch"
have_platform="$(grep -E '^platform=' "$SRC/manifest" | cut -d= -f2 | tr -d '[:space:]')"
if [ "$have_platform" != "$want_platform" ] && [ "$have_platform" != "all" ]; then
  echo "  [错误] manifest platform=$have_platform 与打包架构 $want_platform 不一致"; exit 1
fi
echo "  manifest OK (platform=$have_platform, version=$(grep -E '^version=' "$SRC/manifest"|cut -d= -f2))"

echo "== 2/5 校验 JSON 配置合法性 =="
for f in config/privilege config/resource wizard/install wizard/upgrade wizard/uninstall wizard/config app/ui/config; do
  python3 -c "import json,sys;json.load(open('$SRC/$f'))" || { echo "  [错误] $f 不是合法 JSON"; exit 1; }
  echo "  $f OK"
done

echo "== 3/5 编译后端 (${arch}/${GOARCH}) =="
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" \
  -o "$SRC/app/server/$APPNAME" "$SRC/app/server/main.go"

echo "== 4/5 重建干净打包目录 =="
rm -rf "$BUILD"; mkdir -p "$BUILD"
for d in manifest cmd config wizard; do cp -r "$SRC/$d" "$BUILD/"; done
cp "$SRC/ICON.PNG" "$SRC/ICON_256.PNG" "$BUILD/"
chmod +x "$BUILD"/cmd/*

echo "== 5/5 打 app.tgz + .fpk =="
tar czf "$BUILD/app.tgz" --exclude='server/main.go' -C "$SRC/app" server www ui
tar czf "$ROOT/${APPNAME}-${arch}-${VERSION}.fpk" \
  -C "$BUILD" manifest cmd config wizard ICON.PNG ICON_256.PNG app.tgz

echo "== 结构自检 =="
FPK="$ROOT/${APPNAME}-${arch}-${VERSION}.fpk"
echo "  manifest: platform=$have_platform version=$(grep -E '^version=' "$SRC/manifest"|cut -d= -f2) checkport=$(grep -E '^checkport=' "$SRC/manifest"|cut -d= -f2)"
echo "  cmd 监听: $(tar xzfO "$FPK" cmd/main | grep -o 'app\.sock' | head -1)"
echo "  嵌套残留(cmd/cmd,config/config): $(tar tzf "$FPK" | grep -cE '/cmd/cmd/|/config/config/' || true) 个"
echo "  无 main.go 混入: $(tar tzf "$FPK" | grep -c 'main.go' || true) 个"
echo "  外链CDN残留: $(tar xzfO "$FPK" app.tgz | tar xzfO - www/index.html | grep -c 'https://' || true) 处"
echo "  产物: $FPK"
ls -la "$FPK"
