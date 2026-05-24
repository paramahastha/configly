# Integrations

Configly's HTTP API is simple enough to use from any language without an SDK. This page has copy-paste recipes for the most common environments.

## The universal contract

```
1. Authenticate:  Authorization: Bearer cfly_<your_key>
2. Fetch:         GET /v1/snapshot/{project}/{env}
                  → 200 JSON body with configs map + etag field
3. Cache it in memory.
4. Loop:
     GET /v1/snapshot/{project}/{env}?wait=30
     If-None-Match: <last_etag>
     → 304  unchanged — loop again
     → 200  new data  — replace cache, loop again
5. Reads: lookup key in cached map. Never call the server on a hot path.
```

That's it. The SDKs do this for you. For any other language, it's ~30 lines.

---

## Python

```python
import requests, threading, time

class Configly:
    def __init__(self, url, api_key, project, env):
        self.url     = url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {api_key}"}
        self.project = project
        self.env     = env
        self._snap   = {}
        self._etag   = ""
        self._fetch(wait=0)
        threading.Thread(target=self._loop, daemon=True).start()

    def _fetch(self, wait=0):
        h   = dict(self.headers)
        url = f"{self.url}/v1/snapshot/{self.project}/{self.env}"
        if wait:
            url += f"?wait={wait}"
        if self._etag:
            h["If-None-Match"] = self._etag
        r = requests.get(url, headers=h, timeout=wait + 10 if wait else 10)
        if r.status_code == 200:
            data       = r.json()
            self._snap = data.get("configs", {})
            self._etag = r.headers.get("ETag") or data.get("etag", "")

    def _loop(self):
        while True:
            try:
                self._fetch(wait=30)
            except Exception:
                time.sleep(2)

    def get_string(self, key, default=""):
        e = self._snap.get(key)
        return e["value"] if e else default

    def get_bool(self, key, default=False):
        e = self._snap.get(key)
        return e["value"] == "true" if e else default

    def get_int(self, key, default=0):
        e = self._snap.get(key)
        try:
            return int(e["value"]) if e else default
        except (ValueError, TypeError):
            return default

    def is_enabled(self, key, user_id="", default=False):
        e = self._snap.get(key)
        if not e:
            return default
        rollout = int(e.get("rollout", 0))
        if rollout >= 100:
            return True
        if rollout <= 0 or not user_id:
            return e["value"] == "true"
        return _hash_bucket(f"{key}:{user_id}") < rollout

def _hash_bucket(s: str) -> int:
    h = 0x811c9dc5
    for c in s.encode():
        h ^= c
        h = (h * 0x01000193) & 0xFFFFFFFF
    return h % 100
```

**Usage:**

```python
cfg = Configly("http://localhost:8080", "cfly_...", "default", "prod")

if cfg.is_enabled("new_checkout", user_id):
    # show new checkout
```

---

## Ruby

```ruby
require 'net/http'
require 'json'

class Configly
  def initialize(url, key, project, env)
    @url, @key, @project, @env = url.chomp('/'), key, project, env
    @snap, @etag = {}, ''
    fetch(0)
    Thread.new { loop { begin; fetch(30); rescue; sleep 2; end } }
  end

  def get_string(key, default = '')
    e = @snap[key]; e ? e['value'] : default
  end

  def get_bool(key, default = false)
    e = @snap[key]; e ? e['value'] == 'true' : default
  end

  def is_enabled(key, user_id = '', default = false)
    e = @snap[key]
    return default unless e
    rollout = e['rollout'].to_i
    return true  if rollout >= 100
    return e['value'] == 'true' if rollout <= 0 || user_id.empty?
    hash_bucket("#{key}:#{user_id}") < rollout
  end

  private

  def fetch(wait)
    uri = URI("#{@url}/v1/snapshot/#{@project}/#{@env}#{wait > 0 ? "?wait=#{wait}" : ''}")
    req = Net::HTTP::Get.new(uri)
    req['Authorization'] = "Bearer #{@key}"
    req['If-None-Match'] = @etag unless @etag.empty?
    res = Net::HTTP.start(uri.hostname, uri.port, read_timeout: wait + 10) { |h| h.request(req) }
    return unless res.code == '200'
    data   = JSON.parse(res.body)
    @snap  = data['configs']
    @etag  = res['ETag'] || data['etag'] || ''
  end

  def hash_bucket(s)
    h = 0x811c9dc5
    s.each_byte { |b| h = ((h ^ b) * 0x01000193) & 0xFFFFFFFF }
    h % 100
  end
end
```

**Usage:**

```ruby
cfg = Configly.new('http://localhost:8080', 'cfly_...', 'default', 'prod')
puts cfg.get_string('app_mode', 'standard')
```

---

## Kotlin / Java

```kotlin
import java.net.URI
import java.net.http.*
import org.json.*

class Configly(
    private val url: String,
    private val apiKey: String,
    private val project: String,
    private val env: String,
) {
    @Volatile private var snap: Map<String, JSONObject> = emptyMap()
    @Volatile private var etag = ""
    private val http = HttpClient.newBuilder()
        .connectTimeout(java.time.Duration.ofSeconds(5))
        .build()

    fun start() {
        fetch(0)
        Thread { while (true) runCatching { fetch(30) }.onFailure { Thread.sleep(2000) } }.also { it.isDaemon = true; it.start() }
    }

    fun getString(key: String, default: String = "") =
        snap[key]?.optString("value") ?: default

    fun getBool(key: String, default: Boolean = false) =
        snap[key]?.let { it.optString("value") == "true" } ?: default

    fun isEnabled(key: String, userId: String = "", default: Boolean = false): Boolean {
        val e = snap[key] ?: return default
        val rollout = e.optInt("rollout", 0)
        if (rollout >= 100) return true
        if (rollout <= 0 || userId.isEmpty()) return e.optString("value") == "true"
        return hashBucket("$key:$userId") < rollout
    }

    private fun fetch(wait: Int) {
        val suffix = if (wait > 0) "?wait=$wait" else ""
        val req = HttpRequest.newBuilder(URI("$url/v1/snapshot/$project/$env$suffix"))
            .header("Authorization", "Bearer $apiKey")
            .apply { if (etag.isNotEmpty()) header("If-None-Match", etag) }
            .timeout(java.time.Duration.ofSeconds((wait + 10).toLong()))
            .GET().build()
        val res = http.send(req, HttpResponse.BodyHandlers.ofString())
        if (res.statusCode() == 200) {
            val body = JSONObject(res.body())
            val configs = body.getJSONObject("configs")
            snap = configs.keys().asSequence().associateWith { configs.getJSONObject(it) }
            etag = res.headers().firstValue("ETag").orElse("") .ifEmpty { body.optString("etag") }
        }
    }

    private fun hashBucket(s: String): Int {
        var h = 0x811c9dc5L
        for (b in s.encodeToByteArray()) {
            h = h xor (b.toLong() and 0xFF)
            h = (h * 0x01000193L) and 0xFFFFFFFFL
        }
        return (h % 100).toInt()
    }
}
```

**Usage:**

```kotlin
val cfg = Configly("http://localhost:8080", "cfly_...", "default", "prod")
cfg.start()
if (cfg.isEnabled("new_checkout", userId)) { /* ... */ }
```

Dependencies: `org.json:json` (or swap `JSONObject` for any JSON library you already use).

---

## PHP

```php
<?php
class Configly {
    private array $snap = [];
    private string $etag = '';

    public function __construct(
        private string $url,
        private string $apiKey,
        private string $project,
        private string $env,
    ) {
        $this->fetch(0);
        // In a long-running process (e.g. ReactPHP), call fetch(30) in a loop.
        // In FPM (request-per-process), call fetch(0) on each cold start.
    }

    public function getString(string $key, string $default = ''): string {
        return $this->snap[$key]['value'] ?? $default;
    }

    public function getBool(string $key, bool $default = false): bool {
        return isset($this->snap[$key]) ? ($this->snap[$key]['value'] === 'true') : $default;
    }

    private function fetch(int $wait): void {
        $url  = "{$this->url}/v1/snapshot/{$this->project}/{$this->env}" . ($wait > 0 ? "?wait={$wait}" : '');
        $ctx  = stream_context_create(['http' => [
            'header'  => "Authorization: Bearer {$this->apiKey}\r\n" .
                         ($this->etag ? "If-None-Match: {$this->etag}\r\n" : ''),
            'timeout' => $wait + 10,
        ]]);
        $body = @file_get_contents($url, false, $ctx);
        if ($body === false) return;
        $data       = json_decode($body, true);
        $this->snap = $data['configs'] ?? [];
        $this->etag = $data['etag'] ?? '';
    }
}
```

---

## cURL (shell scripts / CI)

```bash
# One-shot read — good for CI scripts, health checks, or debugging.
KEY=cfly_your_key
URL=http://localhost:8080

curl -s -H "Authorization: Bearer $KEY" \
  "$URL/v1/snapshot/default/prod" | jq '.configs.feature_x.value'
```

---

## Any language: the 30-line template

```
1.  Store: url, apiKey, project, env, snapshot (map), etag (string)
2.  fetch(wait):
      url  = base_url + "/v1/snapshot/" + project + "/" + env
      url += "?wait=" + wait  if wait > 0
      headers = {"Authorization": "Bearer " + apiKey}
      headers["If-None-Match"] = etag  if etag != ""
      resp = HTTP.get(url, headers, timeout=wait+10)
      if resp.status == 200:
        snapshot = resp.json()["configs"]
        etag     = resp.header("ETag") or resp.json()["etag"]
3.  poll_loop():
      while true:
        try: fetch(30)
        catch: sleep(2)
4.  get(key, default):
      return snapshot[key].value  if key in snapshot  else default
5.  start():
      fetch(0)       // seed synchronously
      background(poll_loop)
```

Swap in your language's HTTP client and you're done.

---

## Integration patterns

| Deployment | Recommended approach |
|---|---|
| Long-running service | One client instance at startup; all reads go through the in-memory cache |
| Serverless (Lambda, Cloud Run) | `fetch(0)` on cold start; reuse warm instance |
| Mobile app | One-shot `fetch(0)` on launch; persist to local storage for offline fallback |
| Browser SPA | Use the JS SDK; use a **viewer-scoped** API key (read-only, safe to ship to browsers) |
| CLI / batch job | One `fetch(0)`, read what you need, exit |
| Edge worker | `fetch(0)` at worker init; mild cache invalidation on change events |

## Notes

- **Never call Configly on the hot path.** The whole point is that the snapshot lives in your process memory. Calls to `getString` / `getBool` are nanosecond map lookups.
- **Viewer-scoped keys for browsers.** Any key you ship to a browser is public. Create a `viewer` user in the dashboard and use that key in client-side code.
- **ETag is your friend.** Always send `If-None-Match`. A 304 response costs ~200 bytes and a few milliseconds of server CPU. Without it, every poll downloads the full snapshot.
