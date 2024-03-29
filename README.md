# Nixy for Drove

Nixy is a daemon that automatically configures Nginx for web service containers deployed on the Drove container orchestrator.

It is based off the original Nixy codebase which used to do the same for web services deployed on Apache Mesos and Marathon. Original Nixy Github: https://github.com/martensson/nixy

Features provided by Nixy Open Source
- Real-time updates via Drove's event stream to trigger changes
- Supports full NGinx conf reloads on NGinx OSS as well as only upstream updates using NGinx plus apis
- Automatic service discovery of all apps inside Drove, including their metadata tags as well as health status
- For multi-controller drove setups, will track the leader automatically
- Vhost configuration for leaders
- Single binary with no other dependencies (except Nginx/Openresty)
- Whitelisting of vhost configuration based on suffix and well as host-names

## Usage
Nixy needs TOML a configuration file for managing it's configuration. NGinx configuration is generated from a template file.

### Running with docker

`nixy` is provided as a container. You can run the container as follows:

```shell
docker run --name nx --rm \
    -e DROVE_CONTROLLERS="http://controller1:4000,http://controller2:4000" \
    -e TZ=Asia/Calcutta \
    -e DEBUG=1 \
    -e NGINX_DROVE_VHOST=drove.local \
    --network host \
    quay.io/santanu_sinha/drove-nixy:latest
```
#### Tuning behaviour using environment variables
The following environment variables can be used to tune the behaviour of the container.

| Variable Name     |                           Required                          | Description                                                                                                                    |
|-------------------|:-----------------------------------------------------------:|--------------------------------------------------------------------------------------------------------------------------------|
| DROVE_CONTROLLERS |       **Yes.** List of controllers separated by comma.      | List of individual controller endpoints. Put all controller endpoints here. <br> Nixy will determine the leader automatically. |
| NGINX_DROVE_VHOST | **Optional** The vhost for drove endpoint to be configured. | If this is set, nixy will expose the leader controller over the provided vhost.                                                |
| DROVE_USERNAME    |           **Optional.** Set to `guest` by default.          | Username to login to drove. Read-only user is sufficient.                                                                      |
| DROVE_PASSWORD    |           **Optional.** Set to `guest` by default.          | Password to drove cluster for the above username.                                                                              |
### Running as service

Go through the following steps to run `nixy` as a service.

#### Create the TOML config for Nixy

Sample config file `/etc/nixy/nixy.toml`:

```toml
# Nixy listening port
address = "127.0.0.1"
port = "6000"


# Drove Options

# List of Drove controllers. Add all controller nodes here. Nixy will automatically determine and track th current leader. Auto detection is disabled if a single endpoint is specified.
drove = [
  "http://controller1.mydomain:10000",
   "http://controller1.mydomain:10000"
   ]

# Helps create a vhost entry that tracks the leader on the cluster. Use this to expose the Drove endpoint to users. The value for this will be available to the template engine as the LeaderVHost variable
leader_vhost = "drove-staging.yourdomain-internal.net"

# If some special routing behaviour needs to be implemented in the template based on some tag metadata of the dpeloyed apps, set the routing_tag option to set the tag name to be used. The actual value is derived from app instances and exposed to the template engine as the variable: RoutingTag
routing_tag = "externally_exposed"

# Nixy works on event polling on controller. This is the polling interval. Unless cluster is really busy, this strikes a good balance. Especially if number of NGinx nodes is high. Default is 2 seconds.
event_refresh_interval_sec = 5

# The following two optional params can be used to set basic auth creds if the Drove cluster is basic auth enabled. Leave empty if no basic auth is required.
user = ""
pass = ""

# If cluster has some custom header based auth, the collowing can be used. The contents on thsi parameter are passed verbatim to the Authorization HTTP header. Leave empty if no token auth
access_token = ""

# By default nixy will expose all vhost declared in the spec for all drove apps on a cluster. If specific vhosts need to be exposed, set the realms parameter to a comma separated list of realms. Optional.
realm = "api.yourdomain.com,support.yourdomain.com"

# Beside perfect vhost matching, Nixy supports suffix based matches as well. A single suffix is supported. Optional.
realm_suffix = "-external.yourdomain.com"

# Nginx details

# Path to NGinx config
nginx_config = "/etc/nginx/nginx.conf"

# Path to the template file, based on which the template will be generated
nginx_template = "/etc/nixy/nginx.tmpl"

# NGinx command to use to reload the config
nginx_cmd = "nginx" # optionally "openresty" or "docker exec nginx nginx"

# Ignore calling NGinx command to test the config. Set this to false or delete this line on production. Default: false
nginx_ignore_check = true

# NGinx plus specific options
# If using NGinx plus, set the endpoint to the local server here. If left empty, NGinx plus api based vhost update will be disabled
nginxplusapiaddr="127.0.0.1"

# If specific vhosts are exposed, auto-discovery and updation of config (and NGinx reloads) might not be desired as it will cause connection drops. Set the following parameter to true to disable reloads. Nixy will only update upstreams using the nplus APIs
nginx_reload_disabled=true

# Connection parameters for NGinx plus
maxfailsupstream = 0
failtimeoutupstream = "1s"
slowstartupstream = "0s"

# Statsd settings
#[statsd]
#addr = "10.57.8.171:8125" # optional for statistics
#namespace = "nixy.my_mesos_cluster"
#sample_rate = 100

```

### Create template for NGinx

Create a NGinx template with the following config in `/etc/nixy/nixy.tmpl`

```nginx
# Generated by nixy {{datetime}}

user www-data;
worker_processes auto;
pid /run/nginx.pid;

events {
    use epoll;
    worker_connections 2048;
    multi_accept on;
}
http {
    server_names_hash_bucket_size  128;
    add_header X-Proxy {{ .Xproxy }} always;
    access_log off;
    error_log /var/log/nginx/error.log warn;
    server_tokens off;
    client_max_body_size 128m;
    proxy_buffer_size 128k;
    proxy_buffers 4 256k;
    proxy_busy_buffers_size 256k;
    proxy_redirect off;
    map $http_upgrade $connection_upgrade {
        default upgrade;
        ''      close;
    }
    # time out settings
    proxy_send_timeout 120;
    proxy_read_timeout 120;
    send_timeout 120;
    keepalive_timeout 10;
    
    server {
        listen       7000 default_server;
        server_name  _;
        # Everything is a 503
        location / {
            return 503;
        }
    }
    {{if and .LeaderVHost .Leader.Endpoint}}
    upstream {{.LeaderVHost}} {
        server {{.Leader.Host}}:{{.Leader.Port}}
    }
    server {
        listen 7000;
        server_name {{.LeaderVHost}};
        location / {
            proxy_set_header HOST {{.Leader.Host}};
            proxy_next_upstream error timeout invalid_header http_500 http_502 http_503 http_504;
            proxy_connect_timeout 30;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_pass http://{{.LeaderVHost}}
        }
    }
    {{end}}
    {{- range $id, $app := .Apps}}
    upstream {{$app.Vhost}} {
        {{- range $app.Hosts}}
        server {{ .Host }}:{{ .Port }};
        {{- end}}
    }
    server {
        listen 7000;
        server_name {{$app.Vhost}};
        location / {
            proxy_set_header HOST $host;
            proxy_next_upstream error timeout invalid_header http_500 http_502 http_503 http_504;
            proxy_connect_timeout 30;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_pass http://{{$app.Vhost}};
        }
    }
    {{- end}}
}
```

The above template will do the following:
- Set NGinx port to 7000
- Set up a Vhost `drove-staging.yourdomain-internal.net` that will get auto-updated with the current leader of the Drove cluster
- Setup automatically updated vhosts for all apps on the cluster or those that match the criterion specificed in the `realm` or `realm_suffix` config parameters

> Leader vhost is not updated if NGinx plus is enabled and `nginx_reload_disabled` is set to true.

For more complicated templates, please check `examples` directory

### Nixy SystemD Service configuration

Copy the nixy.service file from `support` directory to `/etc/systemd/system/`.
```shell
cp support/nixy.service /etc/systemd/system/nixy.service
```

Manage the service suing `service` commands.

```shell
service nixy start
service nixy stop
```

#### Checking Logs
You can check logs using:
```shell
journalctl -u nixy -f
```

## Building nixy

There is a script inside `scripts` directory for building the binary. Please run `bash ./gobuild.sh` to build the binary and the docker.

