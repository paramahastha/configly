FROM golang:1.25-alpine AS build
RUN apk add --no-cache build-base sqlite-dev
WORKDIR /src
COPY server/ ./server/
WORKDIR /src/server
RUN go mod download
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /out/configly ./cmd/configly

FROM alpine:3.21
RUN apk add --no-cache ca-certificates sqlite-libs
WORKDIR /data
COPY --from=build /out/configly /usr/local/bin/configly
ENV CONFIGLY_DB=/data/configly.db
ENV CONFIGLY_ADDR=:8080
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/usr/local/bin/configly"]
