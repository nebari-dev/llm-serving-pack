# The console and openmeter-api in one image: openmeter-api answers the
# console's API from OpenMeter and serves the built console.
#
# Build from the repository root, which holds the shared module too:
#   docker build -f usage-console/Dockerfile -t usage-console .

FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS console
WORKDIR /src
COPY usage-console/console/package.json usage-console/console/package-lock.json ./
RUN npm ci --ignore-scripts
COPY usage-console/console/ ./
RUN VITE_DATA=api npm run build

FROM golang:1.26.10-bookworm@sha256:ee3901c61e423732605a51f1b8157c849ec01194ea0a274213b5202b2c11e7bf AS api
WORKDIR /src
COPY shared/ shared/
COPY usage-console/server/go.mod usage-console/server/go.sum usage-console/server/
WORKDIR /src/usage-console/server
RUN go mod download
COPY usage-console/server/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/nebari-dev/nebari-llm-serving-pack/usage-console/server/internal/buildinfo.Version=${VERSION}" -o /openmeter-api ./cmd/openmeter-api

FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
COPY --from=api /openmeter-api /openmeter-api
COPY --from=console /src/dist /console
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/openmeter-api", "-addr", ":8080", "-console-dir", "/console"]
