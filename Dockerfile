# Compiles on the build machine's own platform and cross-compiles to the
# target, so multi-arch builds need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tg-support-bot .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/tg-support-bot /tg-support-bot
ENV DB_PATH=/data/bot.db
VOLUME /data
ENTRYPOINT ["/tg-support-bot"]
