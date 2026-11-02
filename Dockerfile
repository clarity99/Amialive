# syntax=docker/dockerfile:1
# Build on the host's native arch and cross-compile, so arm64 images build fast without QEMU.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/amialive . \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/amialive /amialive
ENV DATA_DIR=/data CONTACTS_FILE=/config/contacts.json
USER 65532:65532
ENTRYPOINT ["/amialive"]
