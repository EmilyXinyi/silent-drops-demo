FROM golang:1.23-alpine AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -o /frontend main.go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /frontend /frontend
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/frontend"]
