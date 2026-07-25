FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY test/fakeupstream ./test/fakeupstream
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/model-velo-fake-upstream ./test/fakeupstream

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/model-velo-fake-upstream /usr/local/bin/model-velo-fake-upstream
USER nonroot:nonroot
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/model-velo-fake-upstream"]
