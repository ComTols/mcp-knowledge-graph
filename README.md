# MCP Knowledge Graph

A persistent, structured memory for AI agents, exposed as an [MCP](https://modelcontextprotocol.io) server.

Instead of dumping notes into one big file, an agent builds a **knowledge graph of documents**: every document has a title, a summary and ordered Markdown blocks, and can sit under one or more parent documents, starting from a single root. Agents can search it semantically, browse it like a tree, and reorganize it over time.

- **Hybrid search**: vector similarity and full-text search, merged with Reciprocal Rank Fusion
- **Hierarchical**: documents can have multiple parents (a DAG), with one primary path
- **Safe concurrent edits**: every write uses optimistic versioning (`expected_version`)
- **Block-level editing**: replace, insert, move or delete single blocks atomically
- **Sources**: attach references (URLs, conversations, files) to documents
- **No LLM inside the server**: the calling agent makes every decision about what to store and where
- **Runs anywhere**: Docker image, PostgreSQL + [pgvector](https://github.com/pgvector/pgvector), Streamable HTTP or stdio

## Quick start with Docker

You need Docker and an API key for an OpenAI-compatible embedding endpoint (OpenAI, Ollama, vLLM, …).

**1. Get the compose file and the example config**

```bash
mkdir mcp-knowledge-graph && cd mcp-knowledge-graph
curl -O https://raw.githubusercontent.com/comtols/mcp-knowledge-graph/main/docker-compose.yml
curl -o .env https://raw.githubusercontent.com/comtols/mcp-knowledge-graph/main/env-template
```

**2. Fill in the secrets in `.env`**

```bash
POSTGRES_PASSWORD=<openssl rand -hex 24>
KG_MCP_BEARER_TOKEN=<openssl rand -hex 32>
KG_EMBEDDING_API_KEY=<your embedding provider key>
```

**3. Start it**

```bash
docker compose up -d
```

The MCP endpoint is now available at `http://localhost:8080/mcp`. The database schema is created automatically on first start.

```bash
curl http://localhost:8080/healthz   # ok
curl http://localhost:8080/readyz    # ready
```

> **Going public?** The server speaks plain HTTP. Put a reverse proxy with TLS (Caddy, nginx, Traefik) in front of it, otherwise your bearer token travels unencrypted.

## Connect your AI client

**Claude Code**

```bash
claude mcp add --transport http knowledge-graph http://localhost:8080/mcp \
  --header "Authorization: Bearer $KG_MCP_BEARER_TOKEN"
```

Or in a project's `.mcp.json` (the token is read from your environment):

```json
{
  "mcpServers": {
    "knowledge-graph": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer ${KG_MCP_BEARER_TOKEN}" }
    }
  }
}
```

Any other MCP client that supports Streamable HTTP with a custom `Authorization` header works the same way.

## Web UI

Next to `/mcp` the server ships a small browser UI (embedded in the binary, no extra service):

| Path | What it does |
|---|---|
| `/ui/` | Interactive graph (Cytoscape.js). Bold blue edges lead to the primary parent, thin dashed orange edges to further parents. Hover a node for its summary, click it to open the document |
| `/ui/viewer?id=<uuid>` | Markdown viewer for one document, with links to its parents and a back button |
| `GET /graph` | The complete graph as Cytoscape.js JSON (`{"elements":{"nodes":[…],"edges":[…]}}`). The primary parent is `data.parent` of a node, every further parent is an edge with the class `secondary-parent` |
| `GET /graph/fulltext?id=<uuid>` | One document as server-rendered Markdown (title, summary, parent and child links, content blocks) |

Everything is protected by the same token as `/mcp`. Open `http://localhost:8080/ui/` in a browser; the login dialog accepts any user name and `KG_MCP_BEARER_TOKEN` as password. Scripts can use `Authorization: Bearer …` instead:

```bash
curl -H "Authorization: Bearer $KG_MCP_BEARER_TOKEN" http://localhost:8080/graph
```

Set `KG_MCP_PUBLIC_URL` (e.g. `https://kg.example.com`) so that the parent links in the Markdown returned by `/graph/fulltext` are absolute and clickable from any client.

## Tools

| Tool | Type | What it does |
|---|---|---|
| `search` | read | Hybrid semantic + keyword search; returns document IDs and their paths |
| `get_root` | read | The root document, the entry point for browsing |
| `get_summary` | read | Title, summary and version of a document |
| `get_children` | read | Child documents with summaries (paginated) |
| `get_parents` | read | Parent documents, primary first |
| `get_paths` | read | Paths from the root to a document |
| `get_fulltext` | read | The ordered Markdown blocks of a document (paginated) |
| `get_sources` | read | Source references of a document |
| `create_document` | write | Create a document with parents, blocks and sources in one step |
| `update_document` | write | Change title and summary |
| `edit_document` | write | Atomic batch of block operations: `replace`, `insert`, `append`, `prepend`, `delete`, `move` |
| `add_parent` / `remove_parent` | write | Link or unlink a document under another one |
| `set_primary_parent` | write | Choose the canonical parent |
| `add_source` | write | Attach a source reference |

Write tools (except `create_document`) require the document's current `expected_version`, so concurrent changes never overwrite each other silently.

## Configuration

All settings are environment variables. With Docker Compose, put them in `.env`.

### Required

| Variable | Description |
|---|---|
| `POSTGRES_PASSWORD` | Password of the PostgreSQL database (Compose only) |
| `KG_MCP_BEARER_TOKEN` | Secret that clients must send as `Authorization: Bearer …`. At least 32 characters |
| `KG_EMBEDDING_API_KEY` | API key for the embedding provider |

### Docker Compose

| Variable | Default | Description |
|---|---|---|
| `KG_IMAGE_TAG` | `latest` | Image version to run, e.g. `0.0.1` |
| `KG_MCP_PUBLISH` | `8080` | Host port of the MCP server |
| `KG_POSTGRES_PORT` | `5433` | Host port of PostgreSQL (bound to `127.0.0.1` only) |
| `POSTGRES_USER` | `kg` | Database user |
| `POSTGRES_DB` | `kg` | Database name |

### MCP server

| Variable | Default | Description |
|---|---|---|
| `KG_MCP_TRANSPORT` | `http` | `http` (Streamable HTTP) or `stdio` |
| `KG_MCP_LISTEN_ADDRESS` | `127.0.0.1:8080` | HTTP listen address (set to `0.0.0.0:8080` by Compose) |
| `KG_MCP_REQUIRE_AUTH` | `true` | Require the bearer token. Only disable for local development |
| `KG_MCP_TRUSTED_ORIGINS` | – | Comma-separated browser origins allowed to call `/mcp` |
| `KG_MCP_PUBLIC_URL` | – | Public base URL of the server, e.g. `https://kg.example.com` (a path prefix is allowed). Used for the parent links in the web viewer; without it the links are root-relative (`/ui/viewer?id=…`) |
| `KG_MCP_MAX_REQUEST_BYTES` | `2097152` | Maximum request body size |
| `KG_MCP_REQUEST_TIMEOUT` | `30s` | Timeout of a single tool call |
| `KG_MCP_SHUTDOWN_TIMEOUT` | `10s` | Grace period for in-flight requests on shutdown |
| `KG_MCP_SESSION_TIMEOUT` | `30m` | Close idle MCP sessions after this time (`0` = never) |
| `KG_MCP_RUN_INDEXER` | `true` | Run the embedding worker inside the server process |
| `KG_MCP_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

### Content limits

| Variable | Default | Description |
|---|---|---|
| `KG_MCP_MAX_TITLE_BYTES` | `512` | Maximum document title size |
| `KG_MCP_MAX_SUMMARY_BYTES` | `4096` | Maximum summary size |
| `KG_MCP_MAX_BLOCK_BYTES` | `65536` | Maximum size of one block |
| `KG_MCP_MAX_SOURCE_URI_BYTES` | `2048` | Maximum source URI size |
| `KG_MCP_MAX_SOURCE_TITLE_BYTES` | `512` | Maximum source title size |
| `KG_MCP_MAX_EDIT_OPERATIONS` | `100` | Maximum operations per `edit_document` call |

Oversized writes are rejected, never silently truncated.

### Database

| Variable | Default | Description |
|---|---|---|
| `KG_DATABASE_URL` | `postgres://kg:kg@localhost:5433/kg?sslmode=disable` | PostgreSQL connection URL. Compose builds it from the `POSTGRES_*` variables |

PostgreSQL needs the `vector` extension (the `pgvector/pgvector` image has it).

### Embeddings

Any OpenAI-compatible `/embeddings` endpoint works. The database stores **1024-dimensional** vectors, so the model must produce 1024 dimensions, natively or via `KG_EMBEDDING_DIMENSIONS`.

| Variable | Default | Description |
|---|---|---|
| `KG_EMBEDDING_BASE_URL` | `https://api.openai.com/v1` (Compose) | Base URL of the API |
| `KG_EMBEDDING_MODEL` | `text-embedding-3-small` (Compose) | Embedding model name |
| `KG_EMBEDDING_DIMENSIONS` | `1024` (Compose) | Sent as the `dimensions` parameter; `0` omits it |
| `KG_EMBEDDING_API_KEY` | – | API key, sent as bearer token |
| `KG_EMBEDDING_TIMEOUT` | `60s` | Timeout per embedding request |

Without Compose, the built-in defaults target a local Ollama (`http://localhost:11434/v1`, model `bge-m3`).

### Embedding worker

Documents are embedded asynchronously in the background.

| Variable | Default | Description |
|---|---|---|
| `KG_INDEXING_BATCH_SIZE` | `16` | Jobs processed per iteration |
| `KG_INDEXING_POLL_INTERVAL` | `2s` | Wait time when there is nothing to do |
| `KG_INDEXING_LEASE` | `5m` | How long a job stays reserved by a worker |
| `KG_INDEXING_MAX_ATTEMPTS` | `5` | Attempts before a job is marked failed |
| `KG_INDEXING_BASE_BACKOFF` | `10s` | Delay before the first retry (doubles each time) |
| `KG_INDEXING_MAX_BACKOFF` | `10m` | Upper bound of the retry delay |
| `KG_INDEXING_MAX_INPUT_BYTES` | `0` | Maximum text size sent to the model (`0` = no check) |

### Search tuning

| Variable | Default | Description |
|---|---|---|
| `KG_SEARCH_CANDIDATE_LIMIT` | `50` | Candidates per ranking before fusion |
| `KG_SEARCH_DEFAULT_LIMIT` | `10` | Results when the caller gives no limit |
| `KG_SEARCH_MAX_LIMIT` | `50` | Maximum number of results |
| `KG_SEARCH_RRF_K` | `60` | Rank constant of Reciprocal Rank Fusion |
| `KG_SEARCH_EXACT_TITLE_WEIGHT` | `1` | Weight of exact title matches |
| `KG_SEARCH_HNSW_EF_SEARCH` | `100` | Vector index search depth (not below the candidate limit) |
| `KG_SEARCH_MAX_QUERY_BYTES` | `4000` | Maximum query length |
| `KG_PATHS_MAX_PATHS` | `10` | Maximum alternative paths returned |
| `KG_PATHS_MAX_DEPTH` | `64` | Maximum path depth |

## Running without Docker

```bash
go build -o mcp-knowledge-graph ./cmd/mcp-knowledge-graph
export KG_DATABASE_URL=... KG_MCP_BEARER_TOKEN=... KG_EMBEDDING_API_KEY=...
./mcp-knowledge-graph
```

For clients that launch the server themselves (for example Claude Desktop), use stdio mode. No bearer token is needed there:

```json
{
  "mcpServers": {
    "knowledge-graph": {
      "command": "/path/to/mcp-knowledge-graph",
      "env": {
        "KG_MCP_TRANSPORT": "stdio",
        "KG_DATABASE_URL": "postgres://kg:secret@localhost:5433/kg?sslmode=disable",
        "KG_EMBEDDING_BASE_URL": "https://api.openai.com/v1",
        "KG_EMBEDDING_MODEL": "text-embedding-3-small",
        "KG_EMBEDDING_DIMENSIONS": "1024",
        "KG_EMBEDDING_API_KEY": "..."
      }
    }
  }
}
```

## Documentation

The detailed specifications live in [`docs/`](docs):

- [MCP server contract](docs/knowledge_graph_mcp_server_spec_go.md): tools, validation, errors, auth
- [Database design](docs/knowledge_graph_db_spec_go.md): hierarchy, search, sources
- [Block editing](docs/knowledge_graph_db_block_editing_addendum.md): sparse ordering and embedding jobs

## Releases

Every push to `main` builds the Docker image, publishes it to `ghcr.io/comtols/mcp-knowledge-graph` and creates a GitHub release. Versions start at `v0.0.1`, and the patch number increases with each release.
