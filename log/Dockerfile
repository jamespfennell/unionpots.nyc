FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/log ./cmd/log && go test ./...

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/log /app/log
ENV LOG_ADDR=:8080 LOG_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/app/log"]
CMD ["serve"]
