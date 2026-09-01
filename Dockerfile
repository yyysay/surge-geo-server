# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/surge-geo-server ./cmd/server

FROM alpine:3.23
RUN adduser -D -H -u 10001 app && mkdir -p /data && chown app:app /data
COPY --from=build /out/surge-geo-server /usr/local/bin/surge-geo-server
USER app
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["surge-geo-server"]
CMD ["-data-dir", "/data"]
