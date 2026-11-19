# Multi-stage build: a static Go binary on a distroless, non-root base.
# The build stage runs on the builder's own platform and cross-compiles for each target,
# so a multi-arch build does not compile Go under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/surrealdb-webapp .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/surrealdb-webapp /surrealdb-webapp
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/surrealdb-webapp"]
