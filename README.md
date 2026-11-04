# jdoc

A small Go service that redirects class and member searches to Oracle's JDK Javadocs. Supports JDK 9 onward, using the published search indices.

## Run

Requires Go 1.23 or newer and outbound HTTPS access to `docs.oracle.com`.

```sh
go run . -listen :8080
```

Open `http://localhost:8080/` for the search form. The server starts listening only after all available versions have been indexed. Startup can take a while and requires enough memory to hold every version's class and member indices. Indexing progress is logged to stderr. Failed downloads are retried twice for transient errors; an incomplete startup exits with an error.

To build a binary:

```sh
go build -o jdoc .
./jdoc -listen 127.0.0.1:8080
```

Interrupt or SIGTERM shuts the server down gracefully. There are no external Go dependencies.

## Docker

```sh
docker build -t jdoc .
docker run --rm --name jdoc -p 127.0.0.1:8080:8080 jdoc
```

## Search

The only endpoint is `GET /?q=...`; `HEAD` is also supported. Matching is exact and case-insensitive.

| Query | Meaning |
| --- | --- |
| `optionallong` | Class in the latest indexed JDK |
| `optionallong#orelse` | Member on that class |
| `17 optionallong` | Class in JDK 17 |
| `optionallong 17` | Same, with a version suffix |
| `17 optionallong#orelse` | Member in JDK 17 |
| `java.util.optionallong` | Fully qualified class |
| `map.entry` | Nested type |
| `string#valueof` | All matching overloads |
| `string#valueof(int)` | Specific overload |

For direct URLs, encode `#` as `%23` and spaces as `%20` or `+`. The form handles encoding automatically. For example:

```sh
curl -i --get --data-urlencode 'q=17 optionallong#orelse' http://localhost:8080/
```

One match produces a `302` redirect. Ambiguous classes and overloads produce an HTML choices page. No match or an unindexed version returns `404`; invalid input returns `400`. Other paths return `404`, and unsupported methods return `405`.

The empty search form focuses the search box automatically. On an ambiguous search, the first result receives focus instead. Use Up/Down to move between results, Home/End to jump to the first/last result, and Enter to follow the focused link. Tab navigation still works normally. Arrow-key navigation uses a small inline script authorized by a per-response CSP nonce; links remain usable without JavaScript.

An unversioned query searches **only the latest indexed JDK**, with no fallback for removed APIs. Explicit versions never fall back. Member lookup covers declared indexed members, including fields and constructors, but does not resolve inherited members. For overloads, use the parameter types displayed in the choices page; signatures may vary between Javadoc versions.

## Discovery

At startup, the service loads versions sequentially from JDK 9. For each version it downloads `type-search-index.js`, `package-search-index.js`, and `member-search-index.js`.

Discovery stops when the next version's type index returns `404`, or redirects to another version's Oracle JDK documentation landing page. Oracle uses that specific redirect for unavailable versions. Other redirects, invalid content, and HTTP errors fail discovery rather than marking a version unavailable.

Every 24 hours after startup, the service probes the next version and indexes only missing versions until it reaches an unavailable one. Completed indices are published atomically; existing searches continue uninterrupted. A failed daily check is logged and retried the next day without discarding existing data. Existing versions are never reindexed until restart.

**Availability means valid published Javadocs, not general availability. Early-access documentation can become the default.** Discovery assumes consecutive major versions with published indices starting at 9. There is no disk persistence: restarting downloads everything again.

Retained names, lookup keys, signatures, anchors, and relative class paths use 32-bit IDs into one string table shared across all versions, including versions discovered later. A string-to-ID map deduplicates strings and resolves queries, while a slice resolves IDs back to strings. ID zero represents the empty string. Searches hold a read lock while using the table; indexing takes a write lock when interning strings. Unknown searches never add entries. Each version stores its API root once; full destination URLs are assembled only for search results.

Complete class records are also shared across versions when their fully qualified name, relative path, and entire member map match. The indexing-only cache groups variants by class-name ID and compares their contents exactly. Deduplication happens after a version has been fully built and validated, before publication; existing shared records are never modified. Changed classes get separate records, and their per-version lookup maps still select the correct variant. Member maps and individual overload slices are not deduplicated independently.

## Test

```sh
go test -race ./...
go vet ./...
```

The default suite is offline. To discover every available version and verify representative class pages and method anchors against Oracle:

```sh
JDOC_LIVE=1 go test -run '^TestLiveOracle$' -v -timeout 12m
```

The live test also reports retained Go heap after garbage collection (including the string table) and a fingerprint of searchable destinations for comparing index representations. Heap usage is not the same as process RSS, which also includes runtime overhead and unused resident heap pages.
