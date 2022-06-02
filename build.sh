#!/usr/bin/env bash
OS=("linux" "windows" "android")
ARCH=("arm64" "amd64")

echo "build for ${1}"

for s in "${OS[@]}"; do
  for a in "${ARCH[@]}"; do
    out="spaceship_${s}_${a}"
    if [ "$s" == "windows" ] ;then
      out="${out}.exe"
    fi;
    GOOS=$s GOARCH=$a go build -ldflags "-s -w" -o "$out" "$1"
    chmod +x "$out"
    echo "build process for $s $a complete"
  done
done