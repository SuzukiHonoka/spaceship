#!/usr/bin/env bash
OS=("linux" "windows" "android" "darwin")
ARCH=("arm64" "amd64")
BUILD_DIR="build"


if [ ! -d "$BUILD_DIR" ]
then
    mkdir $BUILD_DIR
fi
cd $BUILD_DIR || exit

echo "build for [${1}]"

for s in "${OS[@]}"; do
  for a in "${ARCH[@]}"; do
    # Skip unsupported combinations
        if [ "$s" == "android" ] && [ "$a" == "amd64" ]; then
          echo "Skipping $s $a (requires CGO and Android SDK setup)"
          continue
        fi

    out="spaceship_${s}_${a}"
    if [ "$s" == "windows" ] ;then
      out="${out}.exe"
    fi

    echo "Building for $s $a..."
    if GOOS=$s GOARCH=$a go build -ldflags "-s -w" -o "$out" "../$1"; then
      chmod +x "$out"
      echo "build process for $s $a complete"
    else
      echo "build process for $s $a failed"
    fi
  done
done