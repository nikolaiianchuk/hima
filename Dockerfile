FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go tool templ generate && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hima ./cmd/hima

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hima /hima
VOLUME /data
ENV HIMA_DATABASE=/data/hima.db
EXPOSE 8080
ENTRYPOINT ["/hima"]
