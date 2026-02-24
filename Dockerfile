FROM --platform=$BUILDPLATFORM golang:1.24 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o koptimizer ./cmd/optimizer
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o koptimizer-mcp ./cmd/mcp
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o koptimizer-dash ./cmd/dashboard

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /workspace/koptimizer /koptimizer
COPY --from=builder /workspace/koptimizer-mcp /koptimizer-mcp
COPY --from=builder /workspace/koptimizer-dash /koptimizer-dash

EXPOSE 8080
EXPOSE 9090
EXPOSE 3000

USER 65532:65532

ENTRYPOINT ["/koptimizer"]
