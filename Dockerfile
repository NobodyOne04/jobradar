# Build a static binary with the toolchain pinned in go.mod.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY docs/api ./docs/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /jobradar ./cmd/jobradar

# Runtime: no shell, no package manager, non-root.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /jobradar /jobradar
EXPOSE 8080
ENTRYPOINT ["/jobradar"]
CMD ["serve"]
