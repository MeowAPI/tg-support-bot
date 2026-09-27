FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/tg-support-bot .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/tg-support-bot /tg-support-bot
ENV DB_PATH=/data/bot.db
VOLUME /data
ENTRYPOINT ["/tg-support-bot"]
