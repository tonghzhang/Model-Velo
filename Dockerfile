FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/model-velo ./cmd/model-velo
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/model-velo-usage-worker ./cmd/model-velo-usage-worker
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/model-velo-admin ./cmd/model-velo-admin
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/model-velo-healthcheck ./cmd/model-velo-healthcheck

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/model-velo /usr/local/bin/model-velo
COPY --from=build /out/model-velo-usage-worker /usr/local/bin/model-velo-usage-worker
COPY --from=build /out/model-velo-admin /usr/local/bin/model-velo-admin
COPY --from=build /out/model-velo-healthcheck /usr/local/bin/model-velo-healthcheck
USER nonroot:nonroot
EXPOSE 8080 9091
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/model-velo-healthcheck"]
ENTRYPOINT ["/usr/local/bin/model-velo"]
