FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/nexus-gateway ./cmd/gateway \
	&& CGO_ENABLED=0 GOOS=linux go build -o /out/nexus-migrator ./cmd/migrator \
	&& CGO_ENABLED=0 GOOS=linux go build -o /out/nexus-worker ./cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/nexus-gateway /nexus-gateway
COPY --from=build /out/nexus-migrator /nexus-migrator
COPY --from=build /out/nexus-worker /nexus-worker
COPY --from=build /src/migrations ./migrations
COPY --from=build /src/ui ./ui

USER nonroot:nonroot
EXPOSE 18081 18082
ENTRYPOINT ["/nexus-gateway"]
