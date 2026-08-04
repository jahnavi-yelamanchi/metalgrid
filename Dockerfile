ARG CMD=apiserver

FROM golang:1.26-alpine AS build
ARG CMD
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bin ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bin /bin/metalgrid
ENTRYPOINT ["/bin/metalgrid"]
