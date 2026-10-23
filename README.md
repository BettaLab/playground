# X API Playground

A standalone local HTTP server that simulates the X API v2 for testing and development without consuming API credits.

## X API compatibility contract (Atalaya fork)

**Non-negotiable:** The goal is to replace this simulator with the official X API
by changing the API base URL and credentials, **without changing Atalaya's API
client, requests, response parsing, or business logic**. The `/2/` HTTP surface
is an X API compatibility boundary, not a custom Playground API.

- Preserve documented X API paths, HTTP methods, query parameters, request
  bodies, authentication, response status codes, JSON shapes, error envelopes,
  and streaming/matching semantics. The provider's behavior is authoritative.
- Never introduce Playground-only fields, headers, query parameters, response
  attributes, or shortcuts in `/2/` that callers must use or interpret. A
  simulator implementation detail must not become an Atalaya dependency.
- Internal extensions are welcome: deterministic fixtures, seeds, state
  persistence, simulated time/rate limits, inspection tools, and management
  endpoints **outside `/2/`**. Configure simulation controls separately from
  the X-compatible request contract; their effects must still produce responses
  consistent with X API semantics.
- Before changing an X-compatible endpoint, consult the **current official X
  documentation** and, where available, sanitized real-provider observations.
  Add contract-focused tests for requests, success/error responses, dry runs,
  limits, and stream events relevant to the change. A passing simulator test
  alone does not prove compatibility with X.
- If X behavior cannot be reproduced or verified, document the discrepancy,
  avoid presenting the simulated behavior as authoritative, and do not add
  application-side workarounds that would break when switching to the real API.

Official reference endpoints for the current rule-management work:

- [Get stream rules](https://docs.x.com/x-api/stream/get-stream-rules)
- [Update stream rules](https://docs.x.com/x-api/stream/update-stream-rules)
- [Get rule counts](https://docs.x.com/x-api/stream/get-stream-rule-counts)

**Known gaps requiring contract verification (Filtered Stream rules):** The
current `GET /2/tweets/search/stream/rules` implementation does not honor
`ids`, `max_results`, or `pagination_token`; `POST .../rules?dry_run=true`
currently adds `meta.dry_run` to its response even though that field is not
part of the documented response shape. Expression validation is intentionally
partial. Do not make Atalaya rely on these differences. Resolve compatibility
gaps against official documentation/provider evidence before claiming parity.

## Features

- X API v2-shaped endpoints for local simulation (per-endpoint compatibility must be verified)
- Stateful operations with in-memory state management
- Optional file-based state persistence across server restarts
- Interactive web UI for exploring and testing endpoints
- Usage tracking - Track API usage
- Request validation against OpenAPI specifications
- Error responses matching real API formats
- Configurable rate limiting simulation
- Streaming support
- CORS support for web applications
- OpenAPI-driven endpoint discovery and handling

## Prerequisites

- **Go 1.21 or later** - [Download Go](https://go.dev/dl/)

## Installation

### Option 1: Install from Source (Recommended)

```bash
# Clone the repository
git clone https://github.com/xdevplatform/playground.git
cd playground

# Build the binary
go build -o playground ./cmd/playground

# Make it executable (optional)
chmod +x playground

# Move to PATH (optional, for global access)
sudo mv playground /usr/local/bin/
```

### Option 2: Install via go install

```bash
go install github.com/xdevplatform/playground/cmd/playground@latest
```

This will install the `playground` binary to `$GOPATH/bin` (or `$HOME/go/bin` by default).

Make sure `$GOPATH/bin` is in your `PATH`:
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

### Option 3: Download Pre-built Binary

Check the [Releases](https://github.com/xdevplatform/playground/releases) page for pre-built binaries for your platform.

### Verify Installation

```bash
playground --help
```

You should see the playground command help output.

## Quick Start

1. **Start the server:**
   ```bash
   playground start
   ```

2. **Access the Web UI:**
   Open http://localhost:8080/playground in your browser

3. **Make API requests:**
   ```bash
   curl -H "Authorization: Bearer test_token" http://localhost:8080/2/users/me
   ```

## Usage

### Commands

- `playground start` - Start the playground server
  - Flags: 
    - `--port` / `-p` (default: 8080) - Port to run the server on
    - `--host` (default: localhost) - Host to bind the server to
    - `--refresh` - Force refresh of OpenAPI spec cache
- `playground status` - Check if a server is running
- `playground refresh` - Refresh OpenAPI spec cache

### Examples

Start on custom port:
```bash
playground start --port 3000 --host 0.0.0.0
```

Start with OpenAPI cache refresh:
```bash
playground start --refresh
```

### Configuration

Configuration files are stored in `~/.playground/`:
- `config.json` - Playground configuration (optional, uses embedded defaults if not present)
- `state.json` - Persistent state (if persistence enabled)
- `examples/` - Custom example responses (optional)
- `.playground-openapi-cache.json` - Cached OpenAPI spec

### Filtered Stream rule simulation

The X-compatible rule endpoints implement the documented listing and mutation
surface: GET filtering by comma-separated numeric `ids`, `max_results` from 1
to 1,000, opaque 16-character base32hex `pagination_token` cursors, batched add,
targeted delete by provider ID or exact value, `?delete_all=true`, and
side-effect-free `?dry_run=true` previews. Responses use the documented
`data`, `errors`, and `meta` fields; dry runs do not add simulator-only fields.

- `GET /2/tweets/search/stream/rules`
- `POST /2/tweets/search/stream/rules`
- `GET /2/tweets/search/stream/rules/counts`

Rule limits are configured with positive integers:

```json
"filtered_stream_rules": {
  "capacity": 1000,
  "max_expression_length": 1024
}
```

The playground models one shared app/project rule store. Endpoint parameters,
public field names/types, page termination, and mutation selection are aligned
with the official rule-management documentation and covered by local HTTP
contract tests. Partial GET behavior for well-formed IDs that do not exist, the
precise provider error wording for every mixed mutation failure, and cursor
lifetime across provider-side changes still require sanitized real-provider
evidence.

Expression validation rejects unbalanced/empty grouping, incomplete Boolean
expressions, blank operator operands, and configured length violations for the
Atalaya subset (`from:`, reply/quote/retweet `is:` filters, `lang:`, media
filters, negation, grouping, OR, and quoted expressions). It is intentionally
not a complete X query parser: acceptance by Playground does not guarantee
acceptance by X.

### Management Endpoints

The playground provides several management endpoints (not part of X API):

- `GET /health` - Server health and statistics
- `GET /rate-limits` - Rate limit configuration and status
- `GET /config` - View current configuration
- `PUT /config/update` - Update configuration (temporary, lost on restart)
- `POST /config/save` - Save configuration to file
- `POST /state/reset` - Reset state to initial seed data
- `GET /state/export` - Export state as JSON
- `POST /state/import` - Import state from JSON
- `POST /state/save` - Manually save state (if persistence enabled)
- `DELETE /state` - Delete all state
- `GET /endpoints` - List all available endpoints

### Usage & Cost Tracking Endpoints

The playground tracks API usage and provides cost estimation endpoints. These endpoints provide the same data shown in the Usage tab of the web UI:

- `GET /api/credits/pricing` - Get pricing configuration for event types and request types
- `GET /api/accounts/{account_id}/usage` - Get usage data (query params: `interval`, `groupBy`)
- `GET /api/accounts/{account_id}/cost` - Get detailed cost breakdown with billing cycle data and time series

**Getting Usage Data Programmatically:**

The `account_id` is your developer account ID, automatically derived from your authentication token. The default token "test" maps to account "0".

```bash
# Get complete cost breakdown (same data as Usage tab)
curl http://localhost:8080/api/accounts/0/cost | jq

# Get event type usage breakdown
curl "http://localhost:8080/api/accounts/0/usage?interval=30days&groupBy=eventType" | jq

# Get request type usage breakdown
curl "http://localhost:8080/api/accounts/0/usage?interval=30days&groupBy=requestType" | jq

# Get pricing configuration
curl http://localhost:8080/api/credits/pricing | jq
```

**Note:** The `/api/accounts/{account_id}/cost` endpoint returns all the data shown in the Usage tab, including:
- Total cost and billing cycle information
- Cost breakdowns by event type and request type
- Daily time series data for charts
- Usage statistics

See [DOCUMENTATION.md](DOCUMENTATION.md) for detailed API documentation.

## Documentation

For detailed documentation, see [DOCUMENTATION.md](DOCUMENTATION.md).

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT

## Links

- **GitHub**: https://github.com/xdevplatform/playground
- **Issues**: https://github.com/xdevplatform/playground/issues

