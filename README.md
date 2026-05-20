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
| `api_timeout` | integer | `...` | Timeout for Drove API calls. |
| `dns_resolution_timeout_sec` | integer | `...` | Timeout in seconds for DNS resolution. DNS resolution is need as certain operations in nginx/haproxy api's do not accept hostname's for upstreams |
| `event_refresh_interval_sec` | integer | `5` | Polling/refresh interval in seconds for Drove controller event streams. |
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
While **NGINX Plus** handles its own state persistence by natively managing state files (e.g., in `/var/lib/nginx/state/`) to ensure upstreams previously added via the runtime API will continue to reflect across reloads/restarts, **HAProxy** lacks this native state persistence via its runtime API. To guarantee persistence without causing unnecessary process interruptions, Nixy ensures the `haproxy.cfg` is always updated with the new upstream hosts whenever a pure API-based upstream update occurs. During these upstream-only updates, HAProxy is explicitly *not* reloaded, but writing the updated configuration to disk guarantees that any subsequent HAProxy restart reliably loads the most current backends. Additionally, HAProxy's global `server-state-file` functionality can be considered to persist state across reloads/restarts when config templating/reload is disabled. The `server-state-file` should be written by a `systemd` service trigger for `haproxy.service` when HAProxy is shutting down, so it can use it to securely populate the upstream state when starting back up. Implementing this server-state dumping functionality inherently into Nixy itself is a planned to-do.

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

