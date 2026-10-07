Name:           drove-gateway
Version:        2.1
# Release field: default to 1 for releases, but can be overridden by workflows for snapshots
# Workflows can pass -D snapshot_release="0.snapshot.branch.hash" at rpmbuild time
Release:        %{?snapshot_release}%{!?snapshot_release:1}%{?dist}
Summary:        Drove gateway daemon for Nginx/HAProxy configuration management
License:        Apache-2.0
URL:            https://github.com/phonepe/drove-gateway

Source0:        %{name}-%{version}.tar.gz

%define build_version %{version}%{?snapshot_release:-%{snapshot_release}}%{!?snapshot_release:%{nil}}

BuildRequires:  gcc
BuildRequires:  golang >= 1.23
BuildRequires:  git
BuildRequires:  systemd-rpm-macros

Requires:       systemd
Requires:       (nginx or haproxy)
Suggests:       nginx-plus

%description
Drove Gateway (Nixy) is a daemon that automatically configures Nginx or HAProxy
for services deployed on the Drove container orchestrator. It monitors the Drove
event stream and dynamically updates proxy configurations in real-time.

%prep
# Tarball contains a 'drove-gateway' top-level directory
%setup -q -n drove-gateway

%build
# Build main package from src/ (module root)
export GO111MODULE=on
export GOPROXY=https://proxy.golang.org,direct
export GOTOOLCHAIN=local

COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
OS_CODENAME=$(rpm --eval '%{dist}' 2>/dev/null | sed 's/^\.//' | sed 's/[^a-zA-Z0-9]//g')
if [ -n "$OS_CODENAME" ]; then
    BUILD_VERSION="%{build_version}~${OS_CODENAME}"
else
    BUILD_VERSION="%{build_version}"
fi
LDFLAGS="-X main.version=${BUILD_VERSION} -X main.date=$(date '+%%Y-%%m-%%dT%%H:%%M:%%S')"
if [ "$COMMIT" != "unknown" ]; then \
    LDFLAGS="$LDFLAGS -X main.commit=$COMMIT"; \
fi
pushd src
go build -mod=mod -v -ldflags="$LDFLAGS" -o ../nixy .
popd

mkdir -p src/rpmbuild/examples
cp -a examples/. src/rpmbuild/examples/

%install
# Create directories
mkdir -p %{buildroot}%{_bindir}
mkdir -p %{buildroot}%{_sysconfdir}/nixy
mkdir -p %{buildroot}%{_unitdir}
mkdir -p %{buildroot}%{_docdir}/%{name}
mkdir -p %{buildroot}%{_docdir}/%{name}/examples

# Install binary
install -m 0755 nixy %{buildroot}%{_bindir}/nixy

# Install systemd service file
install -m 0644 src/rpmbuild/examples/drove.gateway.service %{buildroot}%{_unitdir}/
install -m 0644 src/rpmbuild/examples/nixy.service %{buildroot}%{_unitdir}/

# Install configuration files
install -m 0644 src/nixy.toml %{buildroot}%{_sysconfdir}/nixy/nixy.toml.example

for tmpl in src/*.tmpl; do
    name=$(basename "$tmpl")
    if [ "$name" = "nginx.tmpl" ]; then
        # Only install nginx.tmpl if not present
        if [ ! -f "%{buildroot}%{_sysconfdir}/nixy/nginx.tmpl" ]; then
            install -m 0644 "$tmpl" "%{buildroot}%{_sysconfdir}/nixy/nginx.tmpl"
        fi
    else
        install -m 0644 "$tmpl" "%{buildroot}%{_sysconfdir}/nixy/"
    fi
done
install -m 0644 src/*.conf %{buildroot}%{_sysconfdir}/nixy/ 2>/dev/null || true

# Install documentation and examples
install -m 0644 README.md %{buildroot}%{_docdir}/%{name}/
install -m 0644 src/rpmbuild/examples/*.tmpl %{buildroot}%{_docdir}/%{name}/examples/ 2>/dev/null || true
install -m 0644 src/rpmbuild/examples/nixy.toml %{buildroot}%{_docdir}/%{name}/examples/nixy.toml.example 2>/dev/null || true
mkdir -p %{buildroot}%{_docdir}/%{name}/examples/haproxy.service.d
install -m 0644 src/rpmbuild/examples/haproxy.service.d/10-drove-gateway-reconcile.conf %{buildroot}%{_docdir}/%{name}/examples/haproxy.service.d/ 2>/dev/null || true
mkdir -p %{buildroot}%{_docdir}/%{name}/examples/nginx.service.d
install -m 0644 src/rpmbuild/examples/nginx.service.d/10-drove-gateway-reconcile.conf %{buildroot}%{_docdir}/%{name}/examples/nginx.service.d/ 2>/dev/null || true

%pre
# Create nixy user if it doesn't exist (optional: currently runs as root)
# getent passwd nixy > /dev/null || useradd -r -s /bin/false -d /var/lib/nixy nixy

%post
# Ensure service is enabled on first install.
if [ $1 -eq 1 ] && command -v systemctl >/dev/null 2>&1; then
    systemctl enable drove.gateway.service >/dev/null 2>&1 || true
fi

# Install systemd drop-ins for the proxy units by copying the example files shipped with the
# package. Every proxy (re)start/reload then signals Drove Gateway to reconcile, and (for HAProxy)
# refreshes the drove-managed #DROVE-SERVERS blocks before HAProxy reads its config.
if [ -d /run/systemd/system ]; then
    examples_dir="%{_docdir}/%{name}/examples"
    for unit in haproxy nginx; do
        src_dropin="${examples_dir}/${unit}.service.d/10-drove-gateway-reconcile.conf"
        dropin_dir="/etc/systemd/system/${unit}.service.d"
        dropin_file="$dropin_dir/10-drove-gateway-reconcile.conf"
        if [ -f "$src_dropin" ]; then
            mkdir -p "$dropin_dir"
            install -m 0644 "$src_dropin" "$dropin_file"
        fi
    done
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

# Create configuration directory if it doesn't exist
mkdir -p %{_sysconfdir}/nixy
chmod 755 %{_sysconfdir}/nixy

# Install default configuration if it doesn't exist
if [ ! -f %{_sysconfdir}/nixy/nixy.toml ]; then
    if [ -f %{_sysconfdir}/nixy/nixy.toml.example ]; then
        cp %{_sysconfdir}/nixy/nixy.toml.example %{_sysconfdir}/nixy/nixy.toml
        chmod 644 %{_sysconfdir}/nixy/nixy.toml
    fi
fi

# Ensure templates are readable, but do not overwrite nginx.tmpl if already present
if [ -d %{_sysconfdir}/nixy ]; then
    for tmpl in %{_sysconfdir}/nixy/*.tmpl; do
        [ -e "$tmpl" ] || continue
        name=$(basename "$tmpl")
        if [ "$name" = "nginx.tmpl" ] && [ -f "%{_sysconfdir}/nixy/nginx.tmpl" ]; then
            # Do not overwrite existing nginx.tmpl
            continue
        fi
        chmod 644 "$tmpl" 2>/dev/null || true
    done
fi

# Create state directory for migrations
mkdir -p /var/lib/drove-gateway

# Migration from older nixy.service to drove.gateway.service
if [ -f /var/lib/drove-gateway/.nixy-was-enabled ]; then
    echo "Migrating from nixy.service to drove.gateway.service..."

    # Remove any stale or conflicting /etc/systemd/system/nixy.service
    if [ -e /etc/systemd/system/nixy.service ]; then
        rm -f /etc/systemd/system/nixy.service
    fi

    # Disable the old nixy.service if present
    if [ -e /usr/lib/systemd/system/nixy.service ]; then
        systemctl disable nixy.service 2>/dev/null || true
    fi

    # Ensure drove.gateway.service file exists before enabling
    if [ -e /usr/lib/systemd/system/drove.gateway.service ]; then
        systemctl enable drove.gateway.service
        systemctl daemon-reload
        systemctl start drove.gateway.service
    else
        echo "Warning: /usr/lib/systemd/system/drove.gateway.service not found, skipping enable/start."
    fi

    # Clean up migration marker
    rm -f /var/lib/drove-gateway/.nixy-was-enabled
    rmdir /var/lib/drove-gateway 2>/dev/null || true

    echo "Migration completed: Service is now running as drove.gateway.service"
fi

%systemd_post drove.gateway.service

%preun
# Stop the service before uninstall (except on upgrade)
if [ $1 -eq 0 ]; then
    # This is an uninstall (not an upgrade)
    if systemctl is-active --quiet drove.gateway.service 2>/dev/null; then
        systemctl stop drove.gateway.service || true
    fi
fi

%systemd_preun drove.gateway.service

%postun
%systemd_postun_with_restart drove.gateway.service

# Clean up configuration on uninstall (not upgrade)
if [ $1 -eq 0 ]; then
    # Remove the proxy systemd drop-ins installed by drove-gateway.
    removed=0
    for unit in haproxy nginx; do
        dropin_file="/etc/systemd/system/${unit}.service.d/10-drove-gateway-reconcile.conf"
        if [ -f "$dropin_file" ]; then
            rm -f "$dropin_file"
            rmdir "/etc/systemd/system/${unit}.service.d" 2>/dev/null || true
            removed=1
        fi
    done
    if [ "$removed" = 1 ] && [ -d /run/systemd/system ]; then
        systemctl daemon-reload >/dev/null 2>&1 || true
    fi

    if [ -d %{_sysconfdir}/nixy ]; then
        rm -rf %{_sysconfdir}/nixy
    fi
fi

%files
%doc %{_docdir}/%{name}/README.md
%docdir %{_docdir}/%{name}/examples
%doc %{_docdir}/%{name}/examples/haproxy.tmpl
%doc %{_docdir}/%{name}/examples/nginx.tmpl
%doc %{_docdir}/%{name}/examples/nixy.toml.example
%doc %{_docdir}/%{name}/examples/haproxy.service.d/10-drove-gateway-reconcile.conf
%doc %{_docdir}/%{name}/examples/nginx.service.d/10-drove-gateway-reconcile.conf

%{_bindir}/nixy
%{_unitdir}/drove.gateway.service
%{_unitdir}/nixy.service
%dir %{_sysconfdir}/nixy
%config(noreplace) %{_sysconfdir}/nixy/nixy.toml.example
%config(noreplace) %{_sysconfdir}/nixy/*.tmpl
%config(noreplace) %{_sysconfdir}/nixy/*.conf

%changelog
* Thu Aug 13 2026 vishnu naini <vishnu.naini@phonepe.com> - 2.1-1
- Bump package and source tarball version to 2.1

* Thu Feb 26 2026 vishnu naini <vishnu.naini@phonepe.com> - 2.0-1
- Initial release with deb/rpm packaging
