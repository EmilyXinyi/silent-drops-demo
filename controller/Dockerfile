FROM golang:1.24-alpine AS build
WORKDIR /src
COPY controller/go.mod controller/go.sum ./
RUN go mod download
COPY controller/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /network-health-controller .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /network-health-controller /network-health-controller
USER nonroot:nonroot
ENTRYPOINT ["/network-health-controller"]
