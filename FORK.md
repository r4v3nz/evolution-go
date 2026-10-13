# Fork: fail-closed proxy

Unofficial fork of [Evolution Go](https://github.com/EvolutionAPI/evolution-go),
**not affiliated with or endorsed by Evolution Foundation**. Branch
`fail-closed-proxy` = upstream `main` + the change below. The user interface
and brand assets are unchanged.

## What changes

Upstream connects an instance to WhatsApp **without** its proxy when:

* the proxy configuration cannot be applied (bad host/port/protocol), or
* the proxy rejects the username/password (`Successfully connected without proxy`).

That exposes the server IP to WhatsApp. In this fork an instance either uses
its proxy or does not connect at all (`Proxy problem, NOT connecting without it
(fail-closed)` in the log, and `disconnectReason` starting with `proxy:`).
Also:

* the WhatsApp Web version check (`https://web.whatsapp.com/sw.js`) goes
  through the instance proxy instead of the server's own connection;
* an instance without its own proxy uses `PROXY_HOST`/`PROXY_PORT` (+
  `PROXY_PROTOCOL`/`PROXY_USERNAME`/`PROXY_PASSWORD`) on every start path, not
  only some of them;
* an unknown `PROXY_PROTOCOL`/`protocol` (e.g. a typo) is an error instead of
  silently becoming `http`;
* `PROXY_REQUIRED=true` (new, optional): instances without any proxy do not
  connect;
* a logged-in instance whose proxy is unreachable (e.g. the mobile uplink is
  renewing its IP) is retried automatically, always through the proxy, with
  backoff 10 s → 20 s → … → 2 min (`disconnectReason` "proxy unavailable (retry
  in …)"). Upstream never retries, so a short proxy outage left instances
  offline until reconnected by hand. A rejected password is not retried, and a
  manual reconnect/logout in the meantime cancels the retry.

Only WhatsApp traffic is affected: webhooks, S3/MinIO, link previews and media
downloaded from URLs you send keep using the server connection, as upstream.

Files: `pkg/whatsmeow/service/proxy_failclosed.go` (+ `_test.go`, new),
`pkg/whatsmeow/service/whatsmeow.go`, `pkg/config/config.go`,
`pkg/config/env/env.go` (marked "Fork change"/"fork:").

## Image

`ghcr.io/r4v3nz/evolution-go:fail-closed` (linux/amd64), built by
`.github/workflows/fork-ghcr.yml` on every push to this branch. Same
environment variables as upstream, plus `PROXY_REQUIRED`.

## Updating from upstream

```bash
git fetch upstream && git rebase upstream/main && go test ./pkg/whatsmeow/... && git push --force-with-lease
```

License: Apache 2.0 with the additional conditions in [LICENSE](LICENSE);
trademarks: [TRADEMARKS.md](TRADEMARKS.md).
