#!/usr/bin/env bash
set -euo pipefail

OS=("linux" "windows" "android" "darwin")
ARCH=("arm64" "amd64")
BUILD_DIR="build"

# Microarchitecture-optimized variants per architecture
# amd64 v3: AVX, AVX2, BMI1, BMI2, F16C, FMA, LZCNT, MOVBE, OSXSAVE (Haswell+, ~2013)
# arm64 v8.2: LSE, FP16 (Apple M1+, Cortex-A76+, ~2018)
micro_variant() {
  case "$1" in
    amd64) echo "GOAMD64=v3" ;;
    arm64) echo "GOARM64=v8.2" ;;
    *)     echo "" ;;
  esac
}

micro_suffix() {
  case "$1" in
    amd64) echo "_v3" ;;
    arm64) echo "_v8.2" ;;
    *)     echo "" ;;
  esac
}

mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR" || exit

PKG="${1:?usage: $0 <package>}"
echo "build for [${PKG}]"

build() {
  local os="$1" arch="$2" suffix="$3" env_var="$4"

  local out="spaceship_${os}_${arch}${suffix}"
  [[ "$os" == "windows" ]] && out="${out}.exe"

  echo "Building ${out}..."
  if env GOOS="$os" GOARCH="$arch" ${env_var} go build -ldflags "-s -w" -o "$out" "../${PKG}"; then
    chmod +x "$out"
    echo "  -> ${out} done"
  else
    echo "  -> ${out} FAILED"
    return 1
  fi
}

for s in "${OS[@]}"; do
  for a in "${ARCH[@]}"; do
    # Skip unsupported combinations
    if [[ "$s" == "android" && "$a" == "amd64" ]]; then
      echo "Skipping $s $a (requires CGO and Android SDK setup)"
      continue
    fi

    # Generic build (baseline microarchitecture)
    build "$s" "$a" "" ""

    # Optimized microarchitecture build
    variant=$(micro_variant "$a")
    if [[ -n "$variant" ]]; then
      build "$s" "$a" "$(micro_suffix "$a")" "$variant"
    fi
  done
done