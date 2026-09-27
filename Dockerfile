FROM caddy
WORKDIR /unionpots.nyc
COPY index.html .
COPY images images
ENTRYPOINT caddy file-server --listen :8080 --root /unionpots.nyc
