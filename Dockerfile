FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/corvus ./cmd/corvus

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/corvus /corvus
EXPOSE 4000
ENTRYPOINT ["/corvus"]
