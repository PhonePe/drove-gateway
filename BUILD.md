# Building Drove Gateway Packages

This document describes how to build Debian (.deb) and RPM (.rpm) packages for drove-gateway.

## Repository Layout

- Go module root: `src/`
- Main package: `src/`
- Debian packaging: `src/debian/`
- RPM packaging: `src/rpmbuild/SPECS/drove-gateway.spec`

## Build Binary

### Prerequisites

Go 1.23+

### Local build

```bash
# From repository root
go -C src mod download
go -C src build -v -o ../nixy .
```

### Local run

```bash
# From repository root
go -C src run .
```

## Build Debian Package (.deb)

### Prerequisites

```bash
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  debhelper-compat \
  devscripts \
  equivs \
  golang-go \
  git
```

### Build commands

```bash
# From repository root
cd src

# Build binary package only
dpkg-buildpackage -us -uc -b

# Build source + binary package
dpkg-buildpackage -us -uc
```

### Debian outputs

- Binary package: `../drove-gateway_*.deb`
- Build log: `../drove-gateway_*.build`
- Source package artifacts (if built): `../drove-gateway_*.dsc`, `../drove-gateway_*.tar.*`

## Build RPM Package (.rpm)

### Prerequisites

```bash
sudo dnf install -y \
  gcc \
  golang \
  git \
  rpm-build \
  rpmdevtools \
  redhat-rpm-config \
  systemd-rpm-macros
```

### Build commands

```bash
# From repository root
BASE_VERSION=$(sed -n '1s/^drove-gateway (\([^)]*\)).*/\1/p' src/debian/changelog | cut -d- -f1)

rpmdev-setuptree
cp src/rpmbuild/SPECS/drove-gateway.spec ~/rpmbuild/SPECS/

tar --exclude='.git' --exclude='.github' --exclude='src/rpmbuild' \
  -czf ~/rpmbuild/SOURCES/drove-gateway-${BASE_VERSION}.tar.gz \
  --transform 's|^\./|drove-gateway/|' \
  -C . .

rpmbuild -ba ~/rpmbuild/SPECS/drove-gateway.spec
```

### RPM outputs

- Binary RPM: `~/rpmbuild/RPMS/$(uname -m)/drove-gateway-*.rpm`
- Source RPM: `~/rpmbuild/SRPMS/drove-gateway-*.src.rpm`

## Build in Containers

### Debian (bookworm)

```bash
docker run --rm -v $(pwd):/work -w /work debian:bookworm bash -c '
  apt-get update && \
  apt-get install -y build-essential debhelper-compat devscripts equivs golang-go git && \
  cd src && \
  dpkg-buildpackage -us -uc -b
'
```

### RPM (UBI9)

```bash
docker run --rm -v $(pwd):/work -w /work quay.io/ubi9/ubi:latest bash -c '
  dnf install -y gcc golang git rpm-build rpmdevtools redhat-rpm-config systemd-rpm-macros && \
  BASE_VERSION=$(sed -n "1s/^drove-gateway (\\([^)]*\\)).*/\\1/p" src/debian/changelog | cut -d- -f1) && \
  rpmdev-setuptree && \
  cp src/rpmbuild/SPECS/drove-gateway.spec ~/rpmbuild/SPECS/ && \
  tar --exclude=".git" --exclude=".github" --exclude="src/rpmbuild" \
    -czf ~/rpmbuild/SOURCES/drove-gateway-${BASE_VERSION}.tar.gz \
    --transform "s|^\\./|drove-gateway/|" -C . . && \
  rpmbuild -ba ~/rpmbuild/SPECS/drove-gateway.spec
'
```

## CI/CD Workflows

- Debian snapshot builds: `.github/workflows/debian-build.yml`
- Debian tag/release builds: `.github/workflows/debian-release.yml`
- RPM snapshot builds: `.github/workflows/rpm-build.yml`
- RPM tag/release builds: `.github/workflows/rpm-release.yml`
- Docker builds: `.github/workflows/docker-build.yml`, `.github/workflows/build-publish.yml`

## Version Sources

- Debian version source: `src/debian/changelog`
- RPM version source: `src/rpmbuild/SPECS/drove-gateway.spec`

Update examples:

```bash
# Debian changelog entry
cd src && dch -i

# RPM spec version field
sed -i 's/^Version:.*/Version:        NEW_VERSION/' src/rpmbuild/SPECS/drove-gateway.spec
```

## Troubleshooting

- `go: cannot find main module`
  - Run Go commands from `src/` or use `go -C src ...`
- `rpmbuild: command not found`
  - Install `rpm-build`
- `dpkg-buildpackage: error: unmet build dependencies`
  - Install dependencies from `src/debian/control` using `mk-build-deps`
