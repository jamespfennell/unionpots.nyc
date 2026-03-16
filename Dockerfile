FROM caddy
WORKDIR /unionpots.nyc
COPY index.html .
ENTRYPOINT caddy file-server --listen :8080 --root /unionpots.nyc
