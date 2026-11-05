# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Cross-compile on the build platform; much faster than emulating arm64.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG TARGETOS TARGETARCH VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/urlshortener .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/urlshortener /urlshortener
ENV LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/urlshortener", "healthcheck"]
ENTRYPOINT ["/urlshortener"]
