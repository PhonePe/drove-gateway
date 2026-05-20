#!/bin/bash -x

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Go module and main package live in src/
cd "${REPO_ROOT}/src"
VERSION="nixy-build-$(date +%Y%m%d%H%M%S)"
go build -ldflags="-X \"main.version=${VERSION}\" -X 'main.date=$(date +"%Y-%m-%d %H:%M:%S")' -X 'main.commit=$(git rev-parse --abbrev-ref HEAD)-$(git rev-parse HEAD)'" -o "${REPO_ROOT}/nixy" .
if [ "$?" -ne 0 ]; then
    echo "Build failure"
    exit 1
fi

echo "Build version details:"

pushd "${REPO_ROOT}/docker"
cp "${REPO_ROOT}/nixy" .
docker build -t ghcr.io/phonepe/drove-gateway:${VERSION} -t  ghcr.io/phonepe/drove-gateway:latest .
popd
