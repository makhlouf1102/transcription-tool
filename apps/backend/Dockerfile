FROM golang:1.25-alpine AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates

COPY apps/backend/go.mod apps/backend/go.sum ./
RUN go mod download

COPY apps/backend/main.go ./
COPY apps/frontend/web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/clearword .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates ffmpeg \
    && addgroup -S app \
    && adduser -S -D -H -G app app \
    && mkdir -p /app/data \
    && chown -R app:app /app

WORKDIR /app
COPY --from=build --chown=app:app /out/clearword /app/clearword
COPY --from=build --chown=app:app /src/web /app/web

ENV HOST=0.0.0.0 \
    PORT=8080

VOLUME ["/app/data"]
EXPOSE 8080
USER app

CMD ["/app/clearword"]
