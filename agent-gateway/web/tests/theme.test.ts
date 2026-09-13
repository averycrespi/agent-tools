import assert from "node:assert/strict";
import { test } from "node:test";
import { readThemePreference, writeThemePreference } from "../src/theme.ts";

const canonical = "agent_gateway_theme";
const legacy = "mcp_gateway_theme";

function storageFixture(initial: Record<string, string>, denied = "") {
  const values = new Map(Object.entries(initial));
  const storage = {
    getItem(key: string) {
      if (denied === "read") throw new Error("denied");
      return values.get(key) ?? null;
    },
    setItem(key: string, value: string) {
      if (denied === "write") throw new Error("denied");
      values.set(key, value);
    },
    removeItem(key: string) {
      if (denied === "remove") throw new Error("denied");
      values.delete(key);
    },
  };
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      get localStorage() {
        if (denied === "access") throw new Error("denied");
        return storage;
      },
    },
  });
  return values;
}

test("theme migration preserves each valid preference across repeated loads", () => {
  for (const value of ["system", "light", "dark"] as const) {
    for (const oldCanonical of [undefined, "invalid"]) {
      const values = storageFixture({
        [legacy]: value,
        ...(oldCanonical === undefined ? {} : { [canonical]: oldCanonical }),
      });
      assert.equal(readThemePreference(), value);
      assert.deepEqual([...values], [[canonical, value]]);
      assert.equal(readThemePreference(), value);
      assert.deepEqual([...values], [[canonical, value]]);
    }
  }
});

test("valid canonical wins and legacy retirement follows successful persistence", () => {
  for (const denied of ["", "write", "remove"]) {
    const values = storageFixture(
      { [canonical]: "light", [legacy]: "dark" },
      denied,
    );
    assert.equal(readThemePreference(), "light");
    assert.equal(values.get(canonical), "light");
    assert.equal(values.has(legacy), denied !== "");
  }
});

test("failed migration retains the only persisted preference and usable choice", () => {
  for (const denied of ["write", "remove"]) {
    const values = storageFixture({ [legacy]: "dark" }, denied);
    assert.equal(readThemePreference(), "dark");
    assert.equal(readThemePreference(), "dark");
    assert.equal(values.get(legacy), "dark");
    assert.equal(
      values.get(canonical),
      denied === "write" ? undefined : "dark",
    );
    writeThemePreference("light");
    assert.equal(values.get(legacy), "dark");
    assert.equal(
      values.get(canonical),
      denied === "write" ? undefined : "light",
    );
  }
});

test("malformed or unavailable storage yields a safe default without throwing", () => {
  for (const denied of ["", "access", "read", "write", "remove"]) {
    storageFixture(
      { [canonical]: "invalid", [legacy]: "also-invalid" },
      denied,
    );
    assert.equal(readThemePreference(), "system");
    assert.doesNotThrow(() => writeThemePreference("light"));
  }
  storageFixture({});
  assert.equal(readThemePreference(), "system");
});
