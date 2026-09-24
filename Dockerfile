FROM golang:1.25-alpine AS build

WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bram-html .

FROM alpine:3.22

RUN addgroup -S bram-html && adduser -S -G bram-html bram-html \
    && mkdir -p /app/data \
    && chown bram-html:bram-html /app/data

WORKDIR /app
COPY --from=build /out/bram-html ./bram-html
COPY frontend/ ./frontend/

ENV ADDR=:8080 \
    BASE_URL=http://localhost:8080 \
    DATA_DIR=/app/data \
    STATIC_DIR=/app/frontend

USER bram-html
EXPOSE 8080
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8080/ | grep -q 'bram-html' || exit 1

ENTRYPOINT ["/app/bram-html"]
