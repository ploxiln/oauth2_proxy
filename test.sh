#!/bin/sh
EXIT_CODE=0

echo "gofmt"
FMTDIFF="$(gofmt -d $(find . -type f -name '*.go'))"
if [ -n "$FMTDIFF" ]; then
    printf '%s\n' "$FMTDIFF"
    EXIT_CODE=1
fi

for pkg in $(go list ./...); do
    echo "testing $pkg"
    echo "go vet $pkg"
    go vet "$pkg" || EXIT_CODE=1
    echo "go test -v $pkg"
    go test -v -timeout 90s "$pkg" || EXIT_CODE=1
    echo "go test -v -race $pkg"
    GOMAXPROCS=4 go test -v -timeout 90s0s -race "$pkg" || EXIT_CODE=1
done

[ $EXIT_CODE = 0 ] || echo "FAIL" 1>&2
exit $EXIT_CODE
