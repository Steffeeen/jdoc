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

The only endpoint is `GET /?q=...`; `HEAD` is also supported. Matching is case-insensitive. Class names are exact; member names fall back to prefix matching when the matched class has no exact member-name match. Explicit signatures remain exact.

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
| `arraylist#ad` | Members whose names start with `ad`, including their overloads |

For example, `arraylist#ad` includes `add`, `addAll`, and other indexed `add...` members. `arraylist#add` matches only the exact `add` overloads, not `addAll`. Prefixes are checked against the matched class's existing member map without retaining an additional prefix index or adding query strings to the string table. Class-name prefixes and substring matching are not supported.

For direct URLs, encode `#` as `%23` and spaces as `%20` or `+`. The form handles encoding automatically. For example:

```sh
curl -i --get --data-urlencode 'q=17 optionallong#orelse' http://localhost:8080/
```

One match produces a `302` redirect. Ambiguous classes and overloads produce an HTML choices page. No match or an unindexed version returns `404`; invalid input returns `400`. Other paths return `404`, and unsupported methods return `405`.

An unversioned query searches **only the latest indexed JDK**, with no fallback for removed APIs. Explicit versions never fall back. 

## Discovery

At startup, the service loads all versions sequentially starting from JDK 9.

Every 24 hours after startup, the service probes the next version and indexes only missing versions until it reaches an unavailable one.

## Test

```sh
go test -race ./...
go vet ./...
```

The default suite is offline. To discover every available version and verify representative class pages and method anchors against Oracle:

```sh
JDOC_LIVE=1 go test -run '^TestLiveOracle$' -v -timeout 12m
```

The live test also reports retained Go heap after garbage collection (including the string table) and a fingerprint of searchable destinations for comparing index representations.
