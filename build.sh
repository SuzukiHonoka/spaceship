#sh
OS=("linux" "windows")
ARCH=("arm64" "amd64")

for s in "${OS[@]}"; do
  for a in "${ARCH[@]}"; do
    out="spaceship_${s}_${a}"
    if [ "$s" == "windows" ] ;then
      out="${out}.exe"
    fi;
    GOOS=$s GOARCH=$a go build -ldflags "-s -w" -o "$out".
    echo "build for $s $a complete"
  done
done