# openmeter-setup: the openmeter subchart's setup Jobs (bootstrap features and
# prices, make the ClickHouse password Secret once).
#
# Build from the repository root, which holds the shared module too:
#   docker build -f openmeter-setup/Dockerfile -t openmeter-setup .

FROM golang:1.26.10-bookworm@sha256:ee3901c61e423732605a51f1b8157c849ec01194ea0a274213b5202b2c11e7bf AS build
WORKDIR /src
COPY shared/ shared/
COPY openmeter-setup/go.mod openmeter-setup/
COPY openmeter-setup/cmd/ openmeter-setup/cmd/
COPY openmeter-setup/internal/ openmeter-setup/internal/
WORKDIR /src/openmeter-setup
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /openmeter-setup ./cmd/openmeter-setup

FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
COPY --from=build /openmeter-setup /openmeter-setup
USER 65532:65532
ENTRYPOINT ["/openmeter-setup"]
