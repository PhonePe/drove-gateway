# Gateway for Drove

Drove gateway works as the gateway to expose the interface for the drove cluster as well as apps/services running on Drove to the outside world.

It is built on top of NGinx and Nixy. Nixy is a daemon that automatically configures Nginx for web service containers deployed on the Drove container orchestrator.

The Nixy code in this repo is based off the original Nixy codebase which used to do the same work for web services deployed on Apache Mesos and Marathon. Original Nixy Github: https://github.com/martensson/nixy

Features provided by Drove Gateway
- Real-time updates via Drove's event stream to trigger changes
- Supports full NGinx conf reloads on NGinx OSS as well as only upstream updates using NGinx plus apis
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

You can run the container using following command for example:

```shell
docker run --name dgw --rm \
    -e DROVE_CONTROLLERS="http://controller1:4000,http://controller2:4000" \
    -e TZ=Asia/Calcutta \
    -e DROVE_USERNAME=guest \
    -e DROVE_PASSWORD=guest \
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

There is a script inside `scripts` directory for building the binary. Please run `bash ./gobuild.sh` to build the binary and the docker.

