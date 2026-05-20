# Packaging Structure

This document describes Debian and RPM packaging for drove-gateway with the current `src/`-based repository layout.

## Directory Structure

```text
drove-gateway/
├── src/
│   ├── go.mod
│   ├── go.sum
│   ├── *.go
│   ├── debian/
│   │   ├── changelog
│   │   ├── control
│   │   ├── install
│   │   ├── postinst
│   │   ├── postrm
│   │   ├── preinst
│   │   ├── rules
│   │   └── source/format
│   └── rpmbuild/
│       ├── SPECS/
│       │   └── drove-gateway.spec
│       └── README.md
├── scripts/
└── .github/workflows/
```

## Package Details

### Common

- Name: `drove-gateway`
- Binary: `/usr/bin/nixy`
- Configuration: `/etc/nixy/`
- Systemd unit: `drove.gateway.service`

### Debian

- Build system: `dpkg-buildpackage` + debhelper
- Packaging files: `src/debian/`
- Primary files:
  - `src/debian/rules`
  - `src/debian/control`
  - `src/debian/changelog`

### RPM

- Build system: `rpmbuild`
- Spec file: `src/rpmbuild/SPECS/drove-gateway.spec`
- Release string may be overridden in CI with `-D snapshot_release ...`

## Build Commands

### Debian (local)

```bash
cd src
dpkg-buildpackage -us -uc -b
```

### RPM (local)

```bash
BASE_VERSION=$(sed -n '1s/^drove-gateway (\([^)]*\)).*/\1/p' src/debian/changelog | cut -d- -f1)
rpmdev-setuptree
cp src/rpmbuild/SPECS/drove-gateway.spec ~/rpmbuild/SPECS/
tar --exclude='.git' --exclude='.github' --exclude='src/rpmbuild' \
  -czf ~/rpmbuild/SOURCES/drove-gateway-${BASE_VERSION}.tar.gz \
  --transform 's|^\./|drove-gateway/|' -C . .
rpmbuild -ba ~/rpmbuild/SPECS/drove-gateway.spec
```

## CI Workflows

- Debian snapshot builds: `.github/workflows/debian-build.yml`
- Debian releases: `.github/workflows/debian-release.yml`
- RPM snapshot builds: `.github/workflows/rpm-build.yml`
- RPM releases: `.github/workflows/rpm-release.yml`

## Version Management

- Debian authoritative version source: `src/debian/changelog`
- RPM spec version source: `src/rpmbuild/SPECS/drove-gateway.spec`

### Update version manually

```bash
cd src && dch -i
sed -i 's/^Version:.*/Version:        NEW_VERSION/' src/rpmbuild/SPECS/drove-gateway.spec
```

## Notes

- Go module files are under `src/`; run Go commands from `src` or use `go -C src ...`.
- Packaging behavior is unchanged functionally; only repository paths changed.
