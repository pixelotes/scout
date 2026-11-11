FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /scout ./cmd/scout

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 scout
COPY --from=build /scout /usr/local/bin/scout
USER scout
ENV SCOUT_LISTEN=:8090 \
    SCOUT_SUBTITLE_CACHE_DIR=/cache/subtitles
VOLUME ["/cache"]
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8090/healthz || exit 1
ENTRYPOINT ["scout"]
CMD ["serve"]
