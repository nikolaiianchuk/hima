FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go tool templ generate && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hima ./cmd/hima
# The runtime image has no shell, so prepare the data directory here.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hima /hima
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME /data
ENV HIMA_DATABASE=/data/hima.db \
    HIMA_CONFIG=/etc/hima/hima.yaml \
    HIMA_LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/hima", "healthcheck"]
ENTRYPOINT ["/hima"]
