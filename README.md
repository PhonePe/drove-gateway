# nixy
Nixy is a daemon that automatically configures Nginx for web services deployed on Apache Mesos and Marathon.
Github: https://github.com/martensson/nixy

Features provided by Nixy Open Source
- Real-time updates via Marathon's event stream (Marathon v0.9.0), so no need for callbacks.
- Automatic service discovery of all running tasks inside Mesos/Marathon, including their health status.
- Single binary with no other dependencies (except Nginx/Openresty)

Features added and planned for future releases
- Nginx Plus API integration to update the upstreams
- Configurable whitelisting based on the traefik.backend label

Features planned for future releases
- Debian package for easy deployment
- Concurrency support when updating nginx upstreams
- Check the cache only for whitelisted marathon applications, right now we make GET calls using nginx plus api even for marathon apps which are not whitelisted.

Bugs:
ERRO[0383] unable to sync from marathon                  error="context deadline exceeded (Client.Timeout or context cancellation while reading body)"


Query the upstreams using nginx plus API

[root@stg-nginx300 ankit.timbadia]# curl -q  -X GET  "http://10.57.11.218/api/6/http/upstreams/nixy-demo/servers" -H "accept: application/json" | jq
[
  {
    "id": 0,
    "server": "10.57.8.94:31326",
    "weight": 1,
    "max_conns": 0,
    "max_fails": 1,
    "fail_timeout": "10s",
    "slow_start": "0s",
    "route": "",
    "backup": false,
    "down": false
  },
  {
    "id": 3,
    "server": "10.57.8.65:31699",
    "weight": 1,
    "max_conns": 0,
    "max_fails": 1,
    "fail_timeout": "10s",
    "slow_start": "0s",
    "route": "",
    "backup": false,
    "down": false
  }
]

State files needs to be created before adding new upstreams in the /etc/nginx/sites-available/upstreams.conf configuration
[root@stg-nginx300 ankit.timbadia]# cat /var/lib/nginx/state/nixy-demo.conf
server 10.57.8.94:31326;
server 10.57.8.65:31699;

nixy-demo upstream defined in the upstream.conf 
[root@stg-nginx300 ankit.timbadia]# cat /etc/nginx/sites-available/upstreams.conf
upstream nixy-demo {
        resolver 127.0.0.1 ipv6=off;
        zone nixy-demo 64k;
        state /var/lib/nginx/state/nixy-demo.conf;
        keepalive 50 ;
}

Dummy nginx virtual host configuration 
server {
        listen 443 ssl;

        ssl_certificate /etc/ssl/certs/phonepe.crt;
        ssl_certificate_key /etc/ssl/certs/phonepe.key;
        ssl_session_cache shared:SSL:20m;
        ssl_session_timeout 5m;

        ssl_prefer_server_ciphers on;

        #ssl_ciphers ECDH+AESGCM:ECDH+AES256:ECDH+AES128:DH+3DES:!ADH:!AECDH:!MD5;
        ssl_ciphers ECDH+AESGCM:ECDH+AES256:ECDH+AES128:!ADH:!AECDH:!MD5:TLS13-CHACHA20-POLY1305-SHA256:TLS13-AES-256-GCM-SHA384:TLS13-AES-128-GCM-SHA256;


        ssl_dhparam /etc/ssl/certs/dhparam.pem;
        #ssl_protocols TLSv1.2;
        ssl_protocols TLSv1.2 TLSv1.3;

        ssl_stapling on;
        ssl_stapling_verify on;
        resolver 127.0.0.1 ipv6=off;

server_name nixy.phonepe.com;


	location / {
           proxy_pass http://nixy-demo;
           proxy_http_version 1.1;
	       proxy_set_header Connection "";
           access_log  /var/log/nginx/access.log;
        }

}


Nixy SystemD service configuration

cat /etc/systemd/system/nixy.service
[root@stg-nginx300 system]# cat nixy.service
[Unit]
Description=nixy - nginx marathon/mesos proxy
After=network.target
StartLimitIntervalSec=0
[Service]
LimitNOFILE=65000
LimitNPROC=65000
Type=simple
Restart=always
RestartSec=1
User=root
ExecStart=/usr/bin/nixy -f /etc/nixy/nixy.toml 2>&1 | logger -t nixy

[Install]
WantedBy=multi-user.target


Start / Stop nixy service
service nixy stop / service nixy start

Logs
journalctl -u nixy -f