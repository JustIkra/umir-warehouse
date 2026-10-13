# syntax=docker/dockerfile:1
FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build-source
WORKDIR /app
ENV GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY bootstrap ./bootstrap
COPY contract ./contract
COPY internal ./internal
COPY tests/fixtures ./tests/fixtures
ARG TARGETOS=linux
ARG TARGETARCH
ARG TOOLCHAIN_IMAGE_ID=sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade
RUN mkdir -p /out/run/umir-fixtures/config /out/run/umir-fixtures/secrets /out/run/umir-fixtures/private && chmod 0700 /out/run/umir-fixtures /out/run/umir-fixtures/config /out/run/umir-fixtures/secrets /out/run/umir-fixtures/private && chown -R 65532:65532 /out/run

FROM build-source AS build
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/fixture-server ./tests/fixtures/server && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/fixture-control ./tests/fixtures/control
RUN go test -count=1 -run TestActualPackagedConstructorAdmission ./tests/fixtures/profiles && TOOLCHAIN_IMAGE_ID=$TOOLCHAIN_IMAGE_ID go run ./tests/fixtures/profiles/provenance && sha256sum /out/fixture-server /out/fixture-control /out/warehouse-provenance.json

FROM build-source AS build-tzdata-missing
COPY tests/fixtures/profiles/data/empty-zoneinfo.zip /app/internal/adapters/zonedata/zoneinfo.zip
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/fixture-server ./tests/fixtures/server && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/fixture-control ./tests/fixtures/control
RUN go test -count=1 -run TestActualPackagedConstructorAdmission ./tests/fixtures/profiles && TOOLCHAIN_IMAGE_ID=$TOOLCHAIN_IMAGE_ID go run ./tests/fixtures/profiles/provenance && sha256sum /out/fixture-server /out/fixture-control /out/warehouse-provenance.json

FROM scratch AS fixture-layout
WORKDIR /app
COPY --from=build-source /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build-source /out/run /run
LABEL org.opencontainers.image.source="https://github.com/JustIkra/umir-warehouse" org.umir.fixture.protocol="umir-operational-fixtures/1.1.0"
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/fixture-server", "healthcheck"]
ENTRYPOINT ["/fixture-server"]
CMD ["serve"]

FROM fixture-layout AS tzdata-missing
COPY --from=build-tzdata-missing /out/fixture-server /fixture-server
COPY --from=build-tzdata-missing /out/fixture-control /fixture-control
COPY --from=build-tzdata-missing /out/warehouse-provenance.json /usr/local/lib/umir/warehouse-provenance.json
CMD ["serve-profile", "--profile", "warehouse-tzdata-missing"]

FROM fixture-layout AS runtime
COPY --from=build /out/fixture-server /fixture-server
COPY --from=build /out/fixture-control /fixture-control
COPY --from=build /out/warehouse-provenance.json /usr/local/lib/umir/warehouse-provenance.json
