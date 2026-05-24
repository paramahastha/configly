// Node native test runner — run with: node --test test/*.test.js
import { test, describe, mock, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { Client, hashBucket } from "../src/index.ts";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeSnapshot(configs = {}) {
  return {
    project: "default",
    environment: "test",
    etag: "abc123",
    configs,
  };
}

function mockFetch(snap, status = 200, etag = snap?.etag ?? "") {
  return mock.fn(async (_url, _opts) => ({
    status,
    ok: status >= 200 && status < 300,
    headers: { get: (k) => (k === "ETag" ? etag : null) },
    json: async () => snap,
    text: async () => JSON.stringify(snap),
  }));
}

function buildClient(overrides = {}) {
  return new Client({
    url: "http://localhost:8080",
    apiKey: "cfly_test",
    project: "default",
    environment: "test",
    ...overrides,
  });
}

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

describe("constructor", () => {
  test("throws when url is missing", () => {
    assert.throws(
      () =>
        new Client({
          url: "",
          apiKey: "k",
          project: "p",
          environment: "e",
        }),
      /url is required/i
    );
  });

  test("throws when apiKey is missing", () => {
    assert.throws(
      () =>
        new Client({
          url: "http://localhost:8080",
          apiKey: "",
          project: "p",
          environment: "e",
        }),
      /apiKey is required/i
    );
  });

  test("throws when project is missing", () => {
    assert.throws(
      () =>
        new Client({
          url: "http://localhost:8080",
          apiKey: "k",
          project: "",
          environment: "e",
        }),
      /project is required/i
    );
  });

  test("throws when environment is missing", () => {
    assert.throws(
      () =>
        new Client({
          url: "http://localhost:8080",
          apiKey: "k",
          project: "p",
          environment: "",
        }),
      /environment is required/i
    );
  });
});

// ---------------------------------------------------------------------------
// Typed accessors
// ---------------------------------------------------------------------------

describe("typed accessors", () => {
  let client;

  beforeEach(async () => {
    const snap = makeSnapshot({
      "feat.str": { type: "string", value: "hello", rollout: 0 },
      "feat.int": { type: "int", value: "42", rollout: 0 },
      "feat.float": { type: "float", value: "3.14", rollout: 0 },
      "feat.bool": { type: "bool", value: "true", rollout: 0 },
      "feat.json": {
        type: "json",
        value: '{"a":1}',
        rollout: 0,
      },
    });
    global.fetch = mockFetch(snap);
    client = buildClient();
    await client.fetchOnce(false);
  });

  test("getString returns correct value", () => {
    assert.equal(client.getString("feat.str", ""), "hello");
  });

  test("getInt returns correct value", () => {
    assert.equal(client.getInt("feat.int", 0), 42);
  });

  test("getFloat returns correct value", () => {
    assert.ok(Math.abs(client.getFloat("feat.float", 0) - 3.14) < 0.001);
  });

  test("getBool returns true for 'true'", () => {
    assert.equal(client.getBool("feat.bool", false), true);
  });

  test("getJSON parses correctly", () => {
    const out = {};
    const ok = client.getJSON("feat.json", out);
    assert.ok(ok);
    assert.deepEqual(out.value, { a: 1 });
  });
});

// ---------------------------------------------------------------------------
// Missing key — returns default, no throw
// ---------------------------------------------------------------------------

describe("missing key defaults", () => {
  let client;

  beforeEach(async () => {
    global.fetch = mockFetch(makeSnapshot());
    client = buildClient();
    await client.fetchOnce(false);
  });

  test("getString missing → default", () => {
    assert.equal(client.getString("missing", "fallback"), "fallback");
  });

  test("getInt missing → default", () => {
    assert.equal(client.getInt("missing", 99), 99);
  });

  test("getFloat missing → default", () => {
    assert.equal(client.getFloat("missing", 1.5), 1.5);
  });

  test("getBool missing → default", () => {
    assert.equal(client.getBool("missing", true), true);
  });

  test("getJSON missing → false", () => {
    const out = {};
    assert.equal(client.getJSON("missing", out), false);
  });

  test("getInt with non-numeric value → default", () => {
    global.fetch = mockFetch(
      makeSnapshot({ "x": { type: "string", value: "notanumber", rollout: 0 } })
    );
    const c = buildClient();
    // seed snapshot synchronously via fetchOnce
    return c.fetchOnce(false).then(() => {
      assert.equal(c.getInt("x", 7), 7);
    });
  });
});

// ---------------------------------------------------------------------------
// If-None-Match header on second fetchOnce
// ---------------------------------------------------------------------------

describe("ETag short-circuit", () => {
  test("second fetchOnce sends If-None-Match header", async () => {
    const snap = makeSnapshot();
    snap.etag = "etag-xyz";
    const calls = [];
    global.fetch = mock.fn(async (url, opts) => {
      calls.push({ url, headers: opts?.headers ?? {} });
      if (calls.length === 1) {
        return {
          status: 200,
          ok: true,
          headers: { get: (k) => (k === "ETag" ? "etag-xyz" : null) },
          json: async () => snap,
          text: async () => "",
        };
      }
      // Second call — 304
      return {
        status: 304,
        ok: false,
        headers: { get: () => null },
        json: async () => {},
        text: async () => "",
      };
    });

    const client = buildClient();
    await client.fetchOnce(false);
    await client.fetchOnce(false);

    assert.equal(calls.length, 2);
    assert.equal(calls[1].headers["If-None-Match"], "etag-xyz");
  });
});

// ---------------------------------------------------------------------------
// Rollout / isEnabled
// ---------------------------------------------------------------------------

describe("isEnabled", () => {
  test("rollout 100% always enabled", async () => {
    global.fetch = mockFetch(
      makeSnapshot({
        flag: { type: "flag", value: "true", rollout: 100 },
      })
    );
    const c = buildClient();
    await c.fetchOnce(false);
    assert.equal(c.isEnabled("flag", "any-user", false), true);
  });

  test("rollout 0% with value false always disabled", async () => {
    global.fetch = mockFetch(
      makeSnapshot({
        flag: { type: "flag", value: "false", rollout: 0 },
      })
    );
    const c = buildClient();
    await c.fetchOnce(false);
    assert.equal(c.isEnabled("flag", "any-user", true), false);
  });

  test("rollout 30% — ~30% of 10k userIDs enabled", async () => {
    global.fetch = mockFetch(
      makeSnapshot({
        "feature.x": { type: "flag", value: "false", rollout: 30 },
      })
    );
    const c = buildClient();
    await c.fetchOnce(false);

    let enabled = 0;
    for (let i = 0; i < 10000; i++) {
      if (c.isEnabled("feature.x", `user-${i}`, false)) enabled++;
    }
    assert.ok(
      enabled >= 2700 && enabled <= 3300,
      `Expected ~3000 enabled, got ${enabled}`
    );
  });

  test("isEnabled for non-flag type uses value == 'true'", async () => {
    global.fetch = mockFetch(
      makeSnapshot({
        "cfg.bool": { type: "bool", value: "true", rollout: 0 },
      })
    );
    const c = buildClient();
    await c.fetchOnce(false);
    assert.equal(c.isEnabled("cfg.bool", "u1", false), true);
  });

  test("missing flag → returns default", async () => {
    global.fetch = mockFetch(makeSnapshot());
    const c = buildClient();
    await c.fetchOnce(false);
    assert.equal(c.isEnabled("nope", "u1", true), true);
  });
});

// ---------------------------------------------------------------------------
// Cross-language hashBucket consistency check
// ---------------------------------------------------------------------------

describe("hashBucket cross-language", () => {
  // The Go SDK test uses "feature.x:user-42" as the canonical cross-check input.
  // Both SDKs must return the same integer for the rollout to be consistent.
  test("hashBucket matches known Go SDK value for 'feature.x:user-42'", () => {
    // Compute the expected value using the same FNV-1a algorithm
    const input = "feature.x:user-42";
    const result = hashBucket(input);
    // Pin the exact value so any hash divergence from the Go SDK is caught immediately.
    assert.equal(result, 53, `Go SDK produces 53 for this input; JS got ${result}`);
    // Verify stability — same input always produces same output
    assert.equal(hashBucket(input), result);
    assert.equal(hashBucket(input), result);
  });

  test("hashBucket is deterministic", () => {
    const samples = ["a", "test-key:user-1", "feature.x:user-42", "x:y"];
    for (const s of samples) {
      assert.equal(hashBucket(s), hashBucket(s));
    }
  });
});
