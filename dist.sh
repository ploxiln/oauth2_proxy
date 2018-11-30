#!/bin/bash
# build binary distributions for most popular operating systems
set -eu
cd "$(dirname "$0")"
DIR="$(pwd)"
checksum_file="sha256sum.txt"
arch=$(go env GOARCH)
version=$(awk '/const VERSION/ {print $NF}' <version.go | sed 's/"//g')
goversion=$(go version | awk '{print $3}')

rm -rf dist
mkdir -p dist

dep ensure -v

echo "... running tests"
./test.sh

for os in windows linux darwin freebsd; do
    echo "... building v$version for $os/$arch"
    EXT=
    if [ $os = windows ]; then
        EXT=".exe"
    fi
    BUILD=$(mktemp -d ${TMPDIR:-/tmp}/oauth2_proxy.XXXXXX)
    TARGET="oauth2_proxy-$version.$os-$arch.$goversion"
    FILENAME="oauth2_proxy-$version.$os-$arch$EXT"
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 \
        go build -ldflags="-s -w" -o $BUILD/$TARGET/$FILENAME || exit 1
    pushd $BUILD/$TARGET
    shasum -a 256 $FILENAME >>"$DIR/dist/$checksum_file"
    cd .. && tar czvf $TARGET.tar.gz $TARGET
    mv $TARGET.tar.gz $DIR/dist
    popd
done
