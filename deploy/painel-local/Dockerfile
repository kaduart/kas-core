# syntax=docker/dockerfile:1
# Imagem só para o painel local: o KAS Core e o importador de CSV, com shell e wget (já vem no alpine)
# para o passo de carga (seed.sh). Não use em produção (lá vale o Dockerfile da raiz).
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/kas-core ./cmd/kas-core \
 && CGO_ENABLED=0 go build -trimpath -o /out/kas-attribution-import ./cmd/kas-attribution-import \
 && CGO_ENABLED=0 go build -trimpath -o /out/kas-scheduling-seed ./cmd/kas-scheduling-seed

FROM alpine:3.20
COPY --from=build /out/ /usr/local/bin/
COPY testdata/attribution /seed/attribution
COPY deploy/painel-local/config.json /etc/kas/config.json
COPY deploy/painel-local/seed.sh /usr/local/bin/seed.sh
USER 65532:65532
ENTRYPOINT ["kas-core"]
CMD ["-config", "/etc/kas/config.json"]
