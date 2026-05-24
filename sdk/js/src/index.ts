/**
 * @configly/sdk — Browser + Node SDK for the Configly config & feature flag platform.
 *
 * Zero runtime dependencies. get*() never throws. IsEnabled() uses FNV-1a → mod 100
 * for rollout buckets, matching the Go SDK exactly so cross-language rollouts are stable.
 */

export interface ConfigEntry {
  type: string;
  value: string;
  rollout: number;
}

export interface Snapshot {
  project: string;
  environment: string;
  etag: string;
  configs: Record<string, ConfigEntry>;
}

export type Logger = { log: (msg: string) => void };

export interface Options {
  url: string;
  apiKey: string;
  project: string;
  environment: string;
  /** "poll" (default) or "sse". SSE falls back to polling when EventSource is unavailable. */
  mode?: "poll" | "sse";
  /** Seconds to hold the long-poll connection open. Default 30, clamped [1, 60]. */
  pollWaitSeconds?: number;
  /** Called each time a new snapshot is received. */
  onUpdate?: (snapshot: Snapshot) => void;
  logger?: Logger;
}

// FNV-1a → 0..99. Must match the Go SDK implementation exactly.
function hashBucket(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
  }
  return h % 100;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export class Client {
  private readonly opts: Required<
    Omit<Options, "onUpdate" | "logger"> & {
      onUpdate: ((s: Snapshot) => void) | undefined;
      logger: Logger | undefined;
    }
  >;
  private snapshot: Snapshot | null = null;
  private etag = "";
  private stopped = false;
  private es: EventSource | null = null;

  constructor(opts: Options) {
    if (!opts.url) throw new Error("configly: url is required");
    if (!opts.apiKey) throw new Error("configly: apiKey is required");
    if (!opts.project) throw new Error("configly: project is required");
    if (!opts.environment) throw new Error("configly: environment is required");

    let wait = opts.pollWaitSeconds ?? 30;
    if (wait < 1) wait = 1;
    if (wait > 60) wait = 60;

    this.opts = {
      url: opts.url.replace(/\/$/, ""),
      apiKey: opts.apiKey,
      project: opts.project,
      environment: opts.environment,
      mode: opts.mode ?? "poll",
      pollWaitSeconds: wait,
      onUpdate: opts.onUpdate,
      logger: opts.logger,
    };
  }

  /** Fetch snapshot once synchronously, then start the background loop. */
  async start(): Promise<void> {
    await this.fetchOnce(false);
    if (this.opts.mode === "sse") {
      this.startSSE();
    } else {
      this.startPollLoop();
    }
  }

  /** Stop the background loop / SSE connection. */
  stop(): void {
    this.stopped = true;
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  private log(msg: string): void {
    this.opts.logger?.log(`[configly] ${msg}`);
  }

  private snapshotUrl(wait: boolean): string {
    const base = `${this.opts.url}/v1/snapshot/${this.opts.project}/${this.opts.environment}`;
    return wait ? `${base}?wait=${this.opts.pollWaitSeconds}` : base;
  }

  async fetchOnce(useWait: boolean): Promise<void> {
    const url = this.snapshotUrl(useWait);
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.opts.apiKey}`,
    };
    if (this.etag) {
      headers["If-None-Match"] = this.etag;
    }

    const timeout = (this.opts.pollWaitSeconds + 10) * 1000;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);

    try {
      const res = await fetch(url, { headers, signal: controller.signal });
      clearTimeout(timer);

      if (res.status === 304) {
        return;
      }
      if (!res.ok) {
        const text = await res.text().catch(() => "");
        throw new Error(`configly: server returned ${res.status}: ${text}`);
      }

      const snap = (await res.json()) as Snapshot;
      const newEtag = res.headers.get("ETag") || snap.etag || "";
      this.etag = newEtag;
      this.snapshot = snap;
      this.opts.onUpdate?.(snap);
    } catch (err) {
      clearTimeout(timer);
      throw err;
    }
  }

  private async startPollLoop(): Promise<void> {
    let retryMs = 1000;
    while (!this.stopped) {
      try {
        await this.fetchOnce(true);
        retryMs = 1000;
      } catch {
        if (this.stopped) return;
        this.log(`poll error, retrying in ${retryMs}ms`);
        await sleep(retryMs + Math.random() * 250);
        retryMs = Math.min(retryMs * 1.2, 5000);
      }
    }
  }

  private startSSE(): void {
    if (typeof EventSource === "undefined") {
      this.log("EventSource not available, falling back to polling");
      this.startPollLoop();
      return;
    }

    const sseUrl = `${this.opts.url}/v1/stream/${this.opts.project}/${this.opts.environment}?key=${encodeURIComponent(this.opts.apiKey)}`;
    const es = new EventSource(sseUrl);
    this.es = es;

    es.addEventListener("change", () => {
      this.fetchOnce(false).catch((err) => this.log(`fetch after SSE change: ${err}`));
    });

    es.onerror = () => {
      if (this.stopped) es.close();
    };
  }

  private lookup(key: string): ConfigEntry | undefined {
    return this.snapshot?.configs[key];
  }

  getString(key: string, def: string): string {
    return this.lookup(key)?.value ?? def;
  }

  getInt(key: string, def: number): number {
    const e = this.lookup(key);
    if (!e) return def;
    const n = parseInt(e.value, 10);
    return Number.isFinite(n) ? n : def;
  }

  getFloat(key: string, def: number): number {
    const e = this.lookup(key);
    if (!e) return def;
    const n = parseFloat(e.value);
    return Number.isFinite(n) ? n : def;
  }

  getBool(key: string, def: boolean): boolean {
    const e = this.lookup(key);
    if (!e) return def;
    return e.value === "true";
  }

  /** Parses the JSON value for key into out. Returns true on success. */
  getJSON<T>(key: string, out: { value?: T }): boolean {
    const e = this.lookup(key);
    if (!e) return false;
    try {
      out.value = JSON.parse(e.value) as T;
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Returns whether the feature flag key is enabled for userID.
   * Uses FNV-1a → mod 100 for stable rollout buckets — same as the Go SDK.
   */
  isEnabled(key: string, userID: string, def: boolean): boolean {
    const e = this.lookup(key);
    if (!e) return def;
    if (e.type !== "flag") return e.value === "true";
    if (e.rollout >= 100) return true;
    if (e.rollout <= 0) return e.value === "true";
    if (!userID) return e.value === "true";
    return hashBucket(`${key}:${userID}`) < e.rollout;
  }
}

export { hashBucket };
