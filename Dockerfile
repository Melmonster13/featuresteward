FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/featuresteward ./cmd/featuresteward

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/featuresteward /featuresteward
EXPOSE 8080
ENTRYPOINT ["/featuresteward"]
