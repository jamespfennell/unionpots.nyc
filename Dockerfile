FROM caddy
WORKDIR /unionpots.nyc
COPY index.html 404.html ./
COPY images images
COPY Caddyfile /etc/caddy/Caddyfile
ENTRYPOINT ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
