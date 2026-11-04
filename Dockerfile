FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mcp-knowledge-graph ./cmd/mcp-knowledge-graph

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp-knowledge-graph /mcp-knowledge-graph
ENTRYPOINT ["/mcp-knowledge-graph"]
