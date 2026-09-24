FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/featuresteward ./cmd/featuresteward

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/featuresteward /featuresteward
EXPOSE 8080
ENTRYPOINT ["/featuresteward"]
