# Static Go binary on scratch: the image holds only the binary and CA certificates.
FROM golang:1.25.14-alpine AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mnsz-watch .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /mnsz-watch /mnsz-watch
USER 65534:65534
ENTRYPOINT ["/mnsz-watch"]
