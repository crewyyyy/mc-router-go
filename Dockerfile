# Standard mode only. Transparent networking requires Linux host network hooks
# and is intentionally not enabled in this unprivileged container profile.
FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/mc-router ./cmd/mc-router

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build --chown=nonroot:nonroot /out/mc-router /usr/local/bin/mc-router
USER nonroot:nonroot
EXPOSE 25565/tcp
ENTRYPOINT ["/usr/local/bin/mc-router"]
CMD ["--config", "/etc/mc-router/config.toml"]
