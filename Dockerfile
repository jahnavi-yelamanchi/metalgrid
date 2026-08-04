ARG CMD=apiserver

# BUILDPLATFORM (not TARGETPLATFORM) pins the build stage to the builder's
# native arch — buildx cross-compiles Go natively via GOARCH below instead
# of running the whole build under QEMU emulation for the target arch.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG CMD
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/bin ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bin /bin/metalgrid
ENTRYPOINT ["/bin/metalgrid"]
