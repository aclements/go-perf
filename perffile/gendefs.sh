#!/bin/sh

set -e

if [ "$1" = -u ]; then
    update=1
    shift
fi

if [ "$#" != 1 ]; then
    echo "Usage: $0 [-u] <Linux source tree>" 2>&1
    exit 2
fi
linux="$1"

go build ../internal/gendefs

process() {
    ./gendefs -ccflags "-I $linux" $1 > .$1.tmp
    if [ -z "$update" ]; then
        diff -u $1 .$1.tmp || true
        rm .$1.tmp
    else
        mv .$1.tmp $1
    fi
}

process aux-defs.go
process events.go
process format.go
rm gendefs
