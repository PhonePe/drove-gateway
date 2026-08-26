# Gateway for Drove

Drove gateway works as the gateway to expose the interface for the drove cluster as well as apps/services running on Drove to the outside world.

It is built on top of NGinx and Nixy. Nixy is a daemon that automatically configures Nginx for web service containers deployed on the Drove container orchestrator. Support is also available to use HAProxy instead of Nginx.

The Nixy code in this repo is based off the original Nixy codebase which used to do the same work for web services deployed on Apache Mesos and Marathon. Original Nixy Github: https://github.com/martensson/nixy[^1]

Features provided by Drove Gateway
- Real-time updates via Drove's event stream to trigger changes
- Supports full NGinx conf reloads on NGinx OSS as well as only upstream updates using NGinx plus apis
- Support for HAProxy conf reloads and only upstream updates using HAProxy runtime apis
- HAProxy also supports header based routing by using HAProxy runtime apis to avoid excessive reloads 
- Automatic service discovery of all apps inside Drove, including their metadata tags as well as health status
- For multi-controller drove setups, will track the leader automatically
- Vhost configuration for leaders
- Single binary with no other dependencies (except Nginx/Openresty)
- Whitelisting of vhost configuration based on suffix and well as host-names

## Usage
Nixy needs TOML a configuration file for managing it's configuration. NGinx configuration is generated from a template file.

The container behaviour can be tuned and run in two ways:

### Customize using environment variables
The following environment variables can be used to tune the behaviour of the container.


| Variable Name     |                           Required                          | Description                                                                                                                    |
|-------------------|:-----------------------------------------------------------:|--------------------------------------------------------------------------------------------------------------------------------|
| DROVE_CONTROLLERS |       **Yes.** List of controllers separated by comma.      | List of individual controller endpoints. Put all controller endpoints here. <br> Nixy will determine the leader automatically. |
| NGINX_DROVE_VHOST | **Optional** The vhost for drove endpoint to be configured. | If this is set, drove-gateway  will expose the leader controller over the provided vhost.                                                |
| DROVE_USERNAME    |           **Optional.** Set to `guest` by default.          | Username to login to drove. Read-only user is sufficient.                                                                      |
| DROVE_PASSWORD    |           **Optional.** Set to `guest` by default.          | Password to drove cluster for the above username.|
| DROVE_CLUSTER_NAME    |           **Optional.** Set to `default` by default.          | Name of drove cluster.|


You can run the container using following command for example:

```shell
docker run --name dgw --rm \
    -e DROVE_CONTROLLERS="http://controller1:4000,http://controller2:4000" \
    -e TZ=Asia/Calcutta \
    -e DROVE_USERNAME=guest \
    -e DROVE_PASSWORD=guest \
    -e DROVE_CLUSTER_NAME=stage \
    -e NGINX_DROVE_VHOST=drove.local \
    --network host \
    ghcr.io/phonepe/drove-gateway
```

### Override config files completely
Configure the following environment variables and volume mount the config files.

|Variable Name|Required|Description|
|-------------|--------|-------------|
| CONFIG_FILE_PATH | **Yes** | Path to the volume mounted custom TOML file to be used by the gateway-nixy|
| TEMPLATE_FILE_PATH | **Yes** | Path to the custom tmpl file to be used to generate NGinx config |

You can run the container using following command for example:
```shell
docker run --rm --name dgw \
    --volume /path/to/drove/gwconfigs:/etc/drove/gateway:ro \
    -e "CONFIG_FILE_PATH=/etc/drove/gateway/gateway.toml" \
    -e "TEMPLATE_FILE_PATH=/etc/drove/gateway/nginx.tmpl" \
    --network host \
    ghcr.io/phonepe/drove-gateway
```

## Building drove-gateway

Use the helper scripts from repository root:

```bash
bash scripts/gobuild.sh
bash scripts/gorun.sh
```

For Debian/RPM packaging, refer to `BUILD.md` and `PACKAGING.md`.

## Configuration Options (`nixy.toml`)

The complete behavior of Nixy is governed by `nixy.toml`. Below is the complete list of available options that can be configured:

### General Settings
| Key | Type | Default / Example | Description |
|---|---|---|---|
| `address` | string | `"127.0.0.1"` | The IP address for Nixy's internal health/metrics api endpoint to listen on. |
| `port` | string | `"6000"` | The port for Nixy's internal health/metrics api endpoint to listen on. |
| `port_use_tls` | boolean | `false` | Whether to use TLS for Nixy's exposed internal API. |
| `port_tls_certfile` | string | `""` | Path to TLS certificate if `port_use_tls` is true. |
| `port_tls_keyfile` | string | `""` | Path to TLS key file if `port_use_tls` is true. |
| `loglevel` | string | `"info"` | Logging level. Can be `debug`, `info`, `warn`, `error`. |
| `xproxy` | string | `""` | The `X-Proxy` header value to use. Defaults to the hostname if empty. Can be used in template to add custom headers, identify request routing etc. |
| `api_timeout` | integer | `10` | Timeout for Drove API calls. |
| `dns_resolution_timeout_sec` | integer | `...` | Timeout in seconds for DNS resolution. DNS resolution is need as certain operations in nginx/haproxy api's do not accept hostname's for upstreams |
| `event_refresh_interval_sec` | integer | `5` | Polling/refresh interval in seconds for Drove controller event streams. |
| `startup_controller_sync_tries` | integer | `2` | On process startup, how many full fresh-sync attempts Drove Gateway makes before using stale persisted datamanager state for controller-unreachable namespaces. Each attempt uses existing per-controller API timeout behavior. |
| `startup_controller_sync_retry_delay_sec` | integer | `1` | Fixed delay in seconds between startup fresh-sync retry attempts. Values `<= 0` default to `1`. |
| `state_persistence_enabled` | boolean | `true` | Enables writing last successful reconciliation metadata to disk so restarts can reconcile from cached state if Drove is unavailable. In-memory datamanager state is always maintained. |
| `state_persistence_dir` | string | `"/var/lib/drove-gateway"` | Directory where Drove Gateway stores persisted datamanager state (`datamanager-state.json`) when disk persistence is enabled. |
| `proxy_platform` | string | `"nginx"` | Defines the underlying proxy enginbe. Supported: `"nginx"` (default) or `"haproxy"`. |
| `left_delimiter` | string | `""` | Custom left template delimiter for go template parsing (default is `{{`). |
| `right_delimiter` | string | `""` | Custom right template delimiter for go template parsing (default is `}}`). |

### Nginx Specific Settings
| Key | Type | Default / Example | Description |
|---|---|---|---|
| `nginx_config` | string | `"./nginx-test.conf"` | Path to the output configuration file to be written for NGINX. |
| `nginx_template` | string | `"./nginx-header.tmpl"` | Path to the template source file used to generate NGINX config. |
| `nginx_cmd` | string | `"nginx"` | Command used to interact with the target proxy. e.g. `"nginx"`, `"openresty"`, `"docker exec nginx nginx"`. |
| `nginx_ignore_check` | boolean | `false` | Disable verifying the NGINX configuration (i.e., skipping `nginx -t`). Health checks will always show OK. |
| `nginx_reload_disabled` | boolean | `false` | If true, do not issue config reload commands to Nginx upon config changes. |
| `nginxplusapiaddr` | string | `""` | Only used for Nginx Plus upstream dynamic API updates via `/api/x/http/upstreams/`. Format `host:port`. |
| `nginx_max_fails` | integer | `0` | Default `max_fails` passed dynamically for every new upstream server updated via API (also compatible with older config flag `maxfailsupstream`). |
| `nginx_fail_timeout` | string | `"1s"` | Default `fail_timeout` passed dynamically for every new upstream server updated via API (also compatible with older config flag `failtimeoutupstream`). |
| `nginx_slow_start` | string | `"0s"` | Default NGINX `slow_start` passed dynamically for every new upstream server updated via API (also compatible with older config flag `slowstartupstream`). |

### HAProxy Specific Settings
| Key | Type | Default / Example | Description |
|---|---|---|---|
| `haproxy_config` | string | `"/etc/haproxy/haproxy.cfg"`| Path exactly where the generated HAProxy configuration is written. |
| `haproxy_template` | string | `"/etc/haproxy/haproxy.tmpl"`| Path to the input GO Template that renders the HAProxy configuration. |
| `haproxy_cmd` | string | `"haproxy"` | Path or command used to run haproxy checks. |
| `haproxy_reload_cmd` | string | `"systemctl reload haproxy"`| Command triggered to gracefully reload HAProxy logic when config is altered. |
| `haproxy_ignore_check` | boolean | `false` | When true, skips configuration syntax verification (`haproxy -c`). |
| `haproxy_reload_disabled` | boolean | `false` | When true, prevents reloading HAProxy entirely. |
| `haproxysocketaddr` | string | `"/run/haproxy/admin.sock"` | Unix domain socket address providing HAProxy runtime APIs (utilized for dynamic upstream pool modification without restarts). |
| `haproxy_disable_large_backend_count_optimisation` | boolean | `false` | Disables the optimization that uses a single aggregated `show servers state` call to fetch all backend states at once. By default, to avoid stale data and excessive API overhead (which could take upwards of 200s for 1000+ backends), Nixy uses a custom parser to fetch all backends in one call. Set to true only if there are compatibility issues with future HAProxy versions. |
| `haproxy_server_name_prefix`| string | `"server"` | Prefix used for server names generated during template generation/backend syncs (e.g. `server_<IP>_<PORT>`). |
| `haproxy_server_name_host_port_delimiter`| string | `"_"` | Delimiter placed between host IPs & port names internally when generating unique HAProxy server names. |
| `haproxy_backend_name_separator`| string | `"_"` | Separator applied between the group name prefix and the downstream app ID when forming stable backend names. |
| `haproxy_backend_include_routing_tag_suffix`| boolean| `true` | When true, appends the routing tag as a suffix to the generated backend name to ensure namespace separation. |
| `haproxy_add_server_attributes_string` | string | `...` | Custom runtime proxy string arguments pushed to dynamically instantiated HAProxy servers (since default-server statements are ignored by the runtime API). e.g., `on-marked-down shutdown-sessions`. |
| `haproxy_add_server_ssl_attributes_string`| string | `""` | Specifically tailored runtime proxy arguments used when a dynamically added server has an `https` port type (e.g., `ssl verify required ca-file ca-certificates.crt`). HAProxy runtime doesn't fully support adding all SSL parameters via runtime so ensure base ciphers are statically defined in your globals section. |
| `haproxy_manage_global_server_state_file` | boolean | `false` | When true, drove-gateway writes HAProxy's `show servers state` output to the state file after every successful reconcile, and (via `nixy -sync-haproxy-state-config`) pre-populates the `#DROVE-SERVERS-BEGIN`/`#DROVE-SERVERS-END` blocks in `haproxy.cfg` before HAProxy (re)starts so `load-server-state-from-file` can restore dynamically added servers. Mainly useful with `haproxy_reload_disabled = true`. |
| `haproxy_global_server_state_file_path` | string | `""` | Path to HAProxy's global server state file (e.g. `/var/lib/haproxy/server_state`). Required when `haproxy_manage_global_server_state_file` is enabled. Must match the `server-state-file` directive in `haproxy.cfg`. |

### Drove Namespaces (`[[namespaces]]`)
Multiple namespaces can be configured as a sequence/array.
| Key | Type | Default / Example | Description |
|---|---|---|---|
| `name` | string | `"stage1"` | Friendly identifier of the namespace (internal to Nixy). |
| `drove` | []string| `["http://localhost:8080"]`| Array of fallback/cluster node endpoints in priority order (Drove controllers). |
| `user` | string | `""` | Optional Basic Auth username to interact with Drove. |
| `pass` | string | `""` | Optional Basic Auth password to interact with Drove. |
| `access_token`| string | `""` | Optional Access Token if Drove handles alternative auth patterns. |
| `realm` | string | `""` | Comma-separated list of exact vhosts to whitelist. If set, filters discovered Drove apps strictly upon exactly matching these vhosts. |
| `realm_suffix`| string | `""` | Limits exposed vhosts to only those matching this strict suffix (e.g. `.stg.example.com`). |
| `routing_tag` | string | `""` | A defined routing tag context filter ensuring only tagged apps integrate onto the upstream lists for this LB namespace context. |
| `leader_vhost`| string | `""` | External VHost applied to direct cluster controller UI dashboard/interactions safely. |

### Limiting VHost Exposure
By default, Drove Gateway reads and surfaces incoming events/vhosts for all apps running in the cluster. This is fine for a single global gateway, but heavily distributed systems often demand sharded proxies handling dedicated domains or subsets.

You can leverage `realm` and `realm_suffix` to clamp down exposure:
*   **Whitelisting Exact Vhosts:** Setting `realm = "api.example.com, web.example.com"` ensures the gateway *only* acts upon and generates upstream configs on the exact domain matches.
*   **Suffix Whitelisting:** Setting `realm_suffix = ".internal.example.com"` safely filters the stream and ignores apps belonging to `.external.example.com`, without maintaining hardcoded exact lists.
If both are omitted, all apps from the namespace are accepted. Apps not matching either defined rule are structurally ignored.

## Proxy Platform API Support & Reload Neceesity

While Drove Gateway is natively integrated to auto-discover and re-route endpoints dynamically, its reload behavior varies slightly depending on your chosen proxy platform configuration:

*   **NGINX (OSS):** Relies strictly on full local configuration reloads when *any* topology changes occur (upstreams scaling, or vhosts added/deleted). Minimum supported version is `1.18`.
*   **NGINX Plus:** Utilizes the NGINX HTTP API (`nginxplusapiaddr`) to dynamically add or delete individual upstream servers for any vhost on the fly without an explicit daemon reload. Reloads will be necessary when vhost is added/deleted. Reloads can be disabled if only a specific set of vhosts are whitelisted in realm. Minimum supported version is `r32`.
*   **HAProxy:** Leverages the HAProxy Runtime API via UNIX domain sockets (`haproxysocketaddr`) for dynamic upstream backend modifications to avoid process restarts. Reloads will be necessary when vhost is added/deleted. Reloads can be disabled if only a specific set of vhosts are whitelisted in realm. Minimum supported version is `2.8`.

**Mandatory Full Reloads (Important):**
Regardless of whether NGINX Plus or HAProxy Runtime APIs are enabled, **a full configuration reload is absolutely mandatory whenever a new vhost is added or an existing vhost is deleted**. The dynamic proxy APIs excel at scaling downstream IP pools *within* existing upstream/backend blocks dynamically. However, they lack the capability to provision or destroy the foundational routing rules or the upstream blocks themselves natively—those structural scaffolding changes must be synchronized via an explicit template write and daemon reload.

**State Management & Persistence:**
While **NGINX Plus** handles its own state persistence by natively managing state files (e.g., in `/var/lib/nginx/state/`) to ensure upstreams previously added via the runtime API will continue to reflect across reloads/restarts, **HAProxy** has stricter semantics when using `server-state-file`.

For HAProxy, `server-state-file` + `load-server-state-from-file` can only restore state for server objects that already exist in the loaded configuration.

### HAProxy `server-state-file` Limitations
`server-state-file` is useful but has important limitations in dynamic environments:

* State entries are matched strictly by `be_name` and `srv_name`.
* HAProxy does not create missing servers from the state file.
* If a backend exists in config but does not contain matching `server` lines (or `server-template` slots), the corresponding state rows are ignored on startup.
* Any mismatch in naming scheme (backend/server naming strategy changes) breaks restoration for those entries.
* In fast-changing clusters, maintaining placeholder entries for every runtime-added server becomes operationally fragile.

### Why Signal-Based Reconciliation Is Better
Drove Gateway uses a signal-driven restart hook to reconcile from source-of-truth app state after proxy startup:

* `ExecStartPre` sends `SIGUSR1` to `drove.gateway.service`.
* `ExecStartPost` sends `SIGUSR2` to `drove.gateway.service`.
* `ExecReload` sends `SIGUSR1` and `SIGUSR2` to `drove.gateway.service` during reload lifecycle.
* On `SIGUSR2`, Drove Gateway triggers full reconciliation and re-adds dynamic upstreams via runtime APIs.

Benefits over relying on `server-state-file`:

* No strict dependency on backend/server name parity with prior state files.
* No need to maintain static placeholder server entries just for restoration.
* Rebuilds runtime state from current Drove topology, not possibly stale restart-time artifacts.
* Same operational model works across HAProxy and NGINX Plus restart flows.

### Optional: HAProxy `server-state-file` Management with Managed Config Blocks
For deployments that still want to use HAProxy's native `server-state-file` (for example when `haproxy_reload_disabled = true` and drove-gateway does not render `haproxy.cfg` at all), Drove Gateway can manage both the state file and the `server` entries it depends on.

Enable it with:

```toml
haproxy_manage_global_server_state_file = true
haproxy_global_server_state_file_path   = "/var/lib/haproxy/server_state"
```

How it works:

1. **State file writes:** After every successful `ReconcileAllBackends`, drove-gateway captures HAProxy's `show servers state` output over the runtime API socket and writes it atomically to `haproxy_global_server_state_file_path`, preserving the destination file permissions when the file already exists. This is the equivalent of `echo "show servers state" | socat stdio unix-connect:<socket> > <stateFile>`.
2. **Managed config blocks:** Because `load-server-state-from-file` only restores state for servers that already exist in the loaded config, you place special comment-delimited blocks inside each relevant backend in `haproxy.cfg`:

   ```
   backend be_myapp
       mode http
       #DROVE-SERVERS-BEGIN be_myapp
       #DROVE-SERVERS-END
   ```

   The backend name after `#DROVE-SERVERS-BEGIN` must match the backend the servers belong to. HAProxy ignores these comment lines, so the config remains valid even when a block is empty.
3. **Block population before (re)start/reload:** The command `nixy -sync-haproxy-state-config -f /etc/nixy/nixy.toml` reads the server state file and rewrites the lines between each `#DROVE-SERVERS-BEGIN`/`#DROVE-SERVERS-END` pair with matching `server <name> <addr>:<port>` entries. Entries without a usable address or a valid port are skipped. It is wired into the HAProxy systemd unit via `ExecStartPre` and `ExecReload` (see `examples/haproxy.service.d/10-drove-gateway-reconcile.conf`) so the servers exist in config right before HAProxy parses it, allowing `load-server-state-from-file` to succeed. If no state file exists yet (first ever start), the blocks are simply emptied.

Requirements and notes:

* When `haproxy_manage_global_server_state_file` is enabled together with `haproxy_reload_disabled = true`, at least one `#DROVE-SERVERS-BEGIN`/`#DROVE-SERVERS-END` block is **mandatory** in `haproxy.cfg`; drove-gateway refuses to start otherwise.
* Your `haproxy.cfg` global section must contain `server-state-file <path>` and each backend `load-server-state-from-file global`, with `<path>` matching `haproxy_global_server_state_file_path`.
* **Reload ordering:** systemd *appends* drop-in `ExecReload=` lines after the base `haproxy.service` reload commands, so the drop-in resets `ExecReload=` (with an empty line) and redeclares the sequence — sync, then HAProxy's own validate + `kill -USR2`, then the reconcile signal — so the config is populated **before** HAProxy re-reads it. The reproduced HAProxy reload commands must match your distro's base unit (the packaged drop-ins use the Debian/RHEL defaults; verify if you override `haproxy.service`).
* Health endpoint `/v1/health` exposes a `ServerStateFileUpdate` status, and metric `drove_gateway_server_state_file_update_healthy` reflects the last state file write.

### Why Signal-Based Reconciliation Is Still Preferred by Default
When reloads are enabled, the signal-based reconciliation above is the simpler and more robust default because it rebuilds runtime state from current Drove topology instead of relying on restart-time file parity. Use the managed `server-state-file` approach primarily when reloads are disabled and you need HAProxy to restore dynamic server state on its own.

### DataManager Stale State Handling (Memory + Optional Disk)
When Drove/controller endpoints are temporarily unreachable, Drove Gateway can still reconcile proxy runtime state (for example after `SIGUSR2`) using the last metadata that previously reconciled successfully.

Why this persistence is needed:

* **NGINX Plus:** Upstream server changes done through the NGINX Plus API are persisted by NGINX Plus itself (state files in NGINX's own lifecycle), so runtime upstream state generally survives proxy restarts.
* **NGINX OSS:** Drove Gateway always regenerates and reloads config for upstream topology changes, so runtime API persistence is not the primary problem.
* **HAProxy Runtime API:** Dynamically added/removed servers are not reliably covered by HAProxy `server-state-file` behavior in highly dynamic setups. `server-state-file` restore is strict (`be_name`/`srv_name` parity, pre-existing server objects) and is not a robust source of truth for API-driven server churn.
* **Gateway startup reality:** On first startup (or node reboot), controller data, DNS, network, or auth dependencies may not be immediately available. Persisted DataManager state lets Drove Gateway continue serving a previously known-good routing view until fresh controller data is reachable.

How it works:

* Whenever the DataManager is refreshed from the controllers, Drove Gateway captures a datamanager state snapshot in memory.
* By default, the same snapshot is also persisted to disk at:
    * `/var/lib/drove-gateway/datamanager-state.json`
* On startup, Drove Gateway first tries to fetch fresh metadata from controllers for `startup_controller_sync_tries` attempts (default `2`).
* Startup retry attempts are spaced by `startup_controller_sync_retry_delay_sec` (default `1` second) with fixed delay semantics.
* Each attempt uses the existing controller request timeout behavior; after tries are exhausted, stale persisted datamanager state is restored for controller-unreachable namespaces (when enabled).

Config knobs:

* `state_persistence_enabled = true|false`
    * `true` (default): keep memory snapshot and persist to disk.
    * `false`: keep memory snapshot only; do not read/write snapshot file.
* `startup_controller_sync_tries = 2`
    * Number of startup fresh-sync attempts before stale persisted-state usage is allowed.
* `startup_controller_sync_retry_delay_sec = 1`
    * Delay between startup fresh-sync attempts before stale persisted-state usage is allowed.
* `state_persistence_dir = "/custom/path"`
    * Changes where `datamanager-state.json` is stored.

Operational note:

* Drove namespace connectivity/auth configuration still comes from `nixy.toml`; only dynamic runtime metadata (apps, leaders, known vhosts/backends, timestamps) is restored from the datamanager state snapshot.

Reference drop-ins:

* `examples/haproxy.service.d/10-drove-gateway-reconcile.conf`
* `examples/nginx.service.d/10-drove-gateway-reconcile.conf`

## Template Variables

When Drove Gateway generates the underlying proxy configurations (both `nginx.tmpl` and `haproxy.tmpl`), it passes a rich context object (`RenderingData`) managed recursively by the `DataManager` and local templater into the Go `text/template` engine. These variables can be accessed dynamically to craft highly customized proxy configuration files.

The template evaluates against a central struct containing:

* **Apps Context (`.Apps`)**: A map of active applications keyed by their string identifiers.
    * `.ID` / `.Vhost`: The application's virtual host domain/identifier.
    * `.Hosts`: An array of associated endpoints. Each host consists of `.Host` (IP/Domain), `.Port` (int), and `.PortType` (protocol like "http" or "https").
    * `.Tags`: Meta-data key/value string mappings extracted from the underlying Drove app context.
    * `.Groups`: A mapping of host sub-groups for granular host mapping (e.g. different feature environments with different value for the key identified by the RoutingTag in the Drove tags).

* **Namespaces Context (`.Namespaces`)**: A map referencing settings per configured Drove namespace.
    * `.LeaderVHost`: The targeted leader dashboard VHost.
    * `.Leader`: An object (`.Endpoint`, `.Host`, `.Port`) representing the active Drove cluster controller leader.
    * `.RoutingTag`: Corresponding active routing tag filter configured for the target namespace.

* **Proxy Specific Parameters**:
    * Global: `.Xproxy`, `.ProxyPlatform` and Template delimiters (`.LeftDelimiter`, `.RightDelimiter`).
    * NGINX Overrides: `.NginxMaxFailsUpstream`, `.NginxFailTimeoutUpstream`, `.NginxSlowStartUpstream`.
    * HAProxy Overrides: `.HaproxySocketAddr`,`.HaproxyServerNamePrefix`, `.HaproxyBackendNameSeparator`, `.HaproxyServerNameHostPortSeparator`, `.HaproxyAddServerAttributesString`, and `.HaproxyAddServerSSLAttributesString`.
    
    > **Best Practice**: It is highly recommended to inject these NGINX and HAProxy override variables natively within your proxy templates instead of hardcoding upstream parameter values directly. Using these template variables ensures perfect consistency between the behavior of upstreams dynamically configured via runtime APIs on the fly and the static upstreams written whenever a full daemon configuration reload takes place.

You can iteratively loop over applications directly as `.Apps`:
```gotemplate
{{`{{range $app := .Apps}}`}}
  # Config proxy routing block corresponding securely to {{`{{$app.Vhost}}`}}
{{`{{end}}`}}
```

### Template Functions

The following functions are available inside all templates:

| Function | Signature | Description |
|----------|-----------|-------------|
| `hostport` | `hostport host port` | Returns `host:port` with correct IPv6 bracket notation (e.g. `[2001:db8::1]:8080`). **Use this instead of** `{{ .Host }}:{{ .Port }}` for IPv6-safe configs. |
| `hasPrefix` | `hasPrefix s prefix` | `strings.HasPrefix` |
| `hasSuffix` | `hasSuffix s suffix` | `strings.HasSuffix` |
| `contains` | `contains s substr` | `strings.Contains` |
| `split` | `split s sep` | `strings.Split` |
| `join` | `join list sep` | `strings.Join` |
| `trim` | `trim s cutset` | `strings.Trim` |
| `replace` | `replace s old new n` | `strings.Replace` |
| `tolower` | `tolower s` | `strings.ToLower` |
| `getenv` | `getenv key` | `os.Getenv` |
| `datetime` | `datetime` | Returns current time (`time.Now`). |

#### IPv6 Compatibility

Templates that build server/upstream addresses **must** use `hostport` instead of direct `{{ .Host }}:{{ .Port }}` concatenation. Direct concatenation produces invalid addresses for IPv6 hosts (e.g. `2001:db8::1:8080` instead of `[2001:db8::1]:8080`).

```gotemplate
# Correct — works with both IPv4 and IPv6
server {{ hostport .Host .Port }};

# Incorrect — breaks with IPv6 addresses
server {{ .Host }}:{{ .Port }};
```

All bundled templates ship with `hostport`. If you maintain custom templates, update any `{{ .Host }}:{{ .Port }}` patterns to use `{{ hostport .Host .Port }}`.

## Advanced Configuration Notes & Gotchas

When migrating or setting up Drove Gateway in complex environments, note the following nuances extracted from the code:

* **Proxy Executable Commands**: The `nginx_cmd`, `haproxy_cmd`, and respective reload commands fully support command-line arguments. This is useful if your proxy is containerized (e.g., setting `nginx_cmd = "docker exec nginx nginx"`) or if you use alternative binaries like OpenResty. 
* **Dynamic HAProxy Server Attributes**: Not all `default-server` properties are supported by the HAProxy runtime API. Attributes like `no-check` or `init-addr` are ignored for dynamic servers. It's recommended to include `on-marked-down shutdown-sessions` inside your `haproxy_add_server_attributes_string` to forcefully terminate lingering connections when a backend instance is removed. Also, advanced features like disabling HTTP/2 on the backend can be dynamically pushed using `alpn http/1.1` in this same string.
* **HTTPS/SSL Attributes via Runtime API**: HAProxy runtime APIs cannot inject base SSL contexts (like cipher suites or root certificates) dynamically for `https` upstreams. The static `haproxy.tmpl` configuration must define these globally. The `haproxy_add_server_ssl_attributes_string` exists strictly to append override flags (e.g., `ssl verify required ca-file ca-certificates.crt`) when a server is added.
* **FQDN DNS Resolution**: Dynamic proxy APIs natively manage routing via explicit IP addresses and do not automatically resolve FQDNs actively. Drove Gateway internally resolves FQDNs to IPs before adding them to NGINX Plus or HAProxy. Ensure your host system's local DNS resolver is highly reliable (potentially serving stale records like RFC8767 out-of-bounds) to prevent dropping hosts if internal DNS temporarily flakes. 

## HAProxy Support

Drove-gateway can be configured to use HAProxy instead of NGINX by setting the `proxy_platform="haproxy"` flag in the `nixy.toml` configuration file. Extensive configuration options are provided to tune its behavior, including support for HAProxy Runtime APIs (`haproxysocketaddr`) which allows for dynamically updating servers on the fly without an explicit reload.

Examples setup and template configurations for HAProxy can be found under the `examples/` directory (`haproxy.tmpl` and `haproxy.tmpl_header_based_routing`). To effectively utilize HAProxy with Dynamic Reloads, update the `haproxy_*` options inside the custom TOML file.

---
[^1]: **Note on Naming Conventions:** Although the project is referred to as **Drove Gateway**, the configuration files and internal components currently continue to use older conventions stemming from its origins (e.g., `nixy.toml`). We are actively working on fully migrating all naming conventions to `drove-gateway` in the near future.

