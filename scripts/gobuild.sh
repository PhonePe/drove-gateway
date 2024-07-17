#!/bin/bash -x

pushd ../src/
VERSION="nixy-build-$(date +%Y%m%d%H%M%S)"
go build -ldflags="-X \"main.version=${VERSION}\" -X 'main.date=$(date +"%Y-%m-%d %H:%M:%S")' -X 'main.commit=$(git rev-parse --abbrev-ref HEAD)-$(git rev-parse HEAD)'" -o nixy
if [ "$?" -ne 0 ]; then
    popd
    echo "Build failure"
fi

popd
mv ../src/nixy .
echo "Build version details:"

pushd ../docker
cp ../scripts/nixy .
docker build -t ghcr.io/phonepe/drove-gateway:${VERSION} -t  ghcr.io/phonepe/drove-gateway:latest .
popd
