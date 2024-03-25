#!/bin/bash -x

pushd ../src/
go build -ldflags="-X 'main.version=nixy-build-$(date +%Y%m%d%H%M%S)' -X 'main.date=$(date +"%Y-%m-%d %H:%M:%S")' -X 'main.commit=$(git rev-parse --abbrev-ref HEAD)-$(git rev-parse HEAD)'" -o nixy
popd
mv ../src/nixy .
echo "Build version details:"
nixy -v
