import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeGrant,
  decodePreview,
  object,
  type Grant,
} from "../src/http-policy-response.ts";
const principal = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FAW";
const otherGrant = "01ARZ3NDEKTSV4RRFFQ69G5FAX";
const credential = "01ARZ3NDEKTSV4RRFFQ69G5FAY";
const otherCredential = "01ARZ3NDEKTSV4RRFFQ69G5FAZ";
const ref = (id = grantID) => ({ id, revision: 3 });
function grant(): Grant {
  return {
    id: grantID,
    principal_id: principal,
    revision: "2",
    description: "Policy",
    expires_at: null,
    state: "active",
    created_at: "2026-09-21T00:00:00.000000000Z",
    updated_at: "2026-09-21T00:00:01.000000000Z",
    policy: {
      version: 1,
      type: "allow_requests",
      allow_private: false,
      request: {
        origin: { scheme: "https", host: "example.com", port: 443 },
        methods: { values: ["GET", "POST"] },
        path: { kind: "segment_prefix", value: "/v1" },
      },
    },
  };
}
function preview() {
  return {
    decision: {
      version: 1,
      principal: ref(principal),
      policy_revision: 5,
      default_revision: 2,
      allowed: false,
      reason: "principal_default",
      transport: "request",
    } as Record<string, unknown>,
    default: "block",
    policy_only: true,
    network_verified: false,
    tls_verified: false,
    material_verified: false,
    admission_authority: false,
  };
}
test("canonical HTTP resources preserve all four shapes, nanoseconds and expired evidence", () => {
  for (const host of [
    "example.com",
    "localhost",
    "*.example.com",
    "xn--bcher-kva.example",
    "xn--caf-dma.example",
    "xn--ls8h.example",
    "127.0.0.1",
    "2001:db8::1",
    "::",
    "::ffff:0:0:1",
  ]) {
    const g = grant();
    g.policy.request!.origin.host = host;
    assert.equal(decodeGrant(g), g, host);
  }
  for (const kind of ["allow_tunnel", "block_destination"] as const) {
    const g = grant();
    g.policy = {
      version: 1,
      type: kind,
      destination: { host: "example.com", port: 443 },
      ...(kind === "allow_tunnel" ? { allow_private: true } : {}),
    };
    assert.equal(decodeGrant(g), g);
  }
  const blocked = grant();
  blocked.policy.type = "block_requests";
  delete blocked.policy.allow_private;
  assert.equal(decodeGrant(blocked), blocked);
  for (const description of [
    null,
    "é".repeat(128),
    "😀".repeat(64),
    "\uFEFFlabel",
  ]) {
    const g = grant();
    g.description = description;
    assert.equal(decodeGrant(g), g);
  }
  const g = grant();
  g.created_at = "2026-09-21T00:00:00.123456789Z";
  g.updated_at = "2026-09-21T00:00:00.123456791Z";
  g.expires_at = "2026-09-21T00:00:00.123456790Z";
  g.state = "expired";
  assert.equal(decodeGrant(g), g);
});
test("noncanonical HTTP grants cannot cross the response boundary", () => {
  const edits: Array<(g: Grant) => void> = [
    (g) => {
      g.updated_at = "2026-09-20T23:59:59.000000000Z";
    },
    (g) => {
      g.created_at = "2026-09-21T00:00:00.123456789Z";
      g.updated_at = "2026-09-21T00:00:00.123456788Z";
    },
    (g) => {
      g.created_at = "2026-02-30T00:00:00.000000000Z";
    },
    (g) => {
      g.updated_at = "2026-09-21T00:00:01+00:00";
    },
    (g) => {
      g.updated_at = "2026-09-21T00:00:01.000Z";
    },
    (g) => {
      g.updated_at = "2026-09-21T00:00:01.1234567891Z";
    },
    (g) => {
      g.expires_at = g.created_at;
    },
    (g) => {
      g.state = "expired";
    },
    ...["", " x", "x\u0085", "é".repeat(129), "\uD800", "\nlabel"].map(
      (description) => (g: Grant) => {
        g.description = description;
      },
    ),
    (g) => {
      g.revision = "9223372036854775808";
    },
    (g) => {
      delete g.policy.allow_private;
    },
    (g) => {
      g.policy.request!.methods = { values: ["POST", "GET"] };
    },
    (g) => {
      g.policy.request!.methods = { values: ["GET", "GET"] };
    },
    (g) => {
      g.policy.request!.methods = { values: ["CONNECT"] };
    },
    (g) => {
      g.policy.request!.methods = { values: ["get"] };
    },
    (g) => {
      g.policy.request!.origin.scheme = "http";
      g.policy.credential_id = credential;
    },
    ...[
      "EXAMPLE.com",
      "ab--cd.example",
      "127.1",
      "010.0.0.1",
      "*.127.0.0.1",
      "*.localhost",
      "::ffff:7f00:1",
      "example.com.",
      "0x7f000001",
      "foo.123",
      "xn--invalid-",
      "xn--a.example",
      "xn--0.example",
      "xn---bcher-kva.example",
      "xn--a-ecp.example",
      "xn--not-puny.example",
      "xn--ib9b.example",
      "a".repeat(64) + ".com",
    ].map((host) => (g: Grant) => {
      g.policy.request!.origin.host = host;
    }),
    ...[
      "/a/../b",
      "/%61",
      "/a//b",
      "/v1/",
      "/a?x=1",
      "/é",
      "/" + "a".repeat(4096),
    ].map((value) => (g: Grant) => {
      g.policy.request!.path.value = value;
    }),
  ];
  for (const [i, edit] of edits.entries()) {
    const g = grant();
    edit(g);
    assert.throws(() => decodeGrant(g), String(i));
  }
  for (const key of Object.keys(grant())) {
    const g = object(grant());
    delete g[key];
    assert.throws(() => decodeGrant(g), key);
  }
  assert.throws(() => decodeGrant({ ...grant(), unknown: true }));
});
test("policy-only preview accepts every actual selection outcome with revision evidence", () => {
  const cases: Array<
    [string, boolean, (p: ReturnType<typeof preview>) => void]
  > = [
    ["default block", false, () => {}],
    [
      "forbidden literal",
      false,
      (p) => {
        p.default = "allow";
        p.decision.reason = "address_forbidden";
      },
    ],
    [
      "private literal",
      false,
      (p) => {
        p.default = "allow";
        p.decision.reason = "private_permission_required";
      },
    ],
    [
      "forbidden tunnel",
      true,
      (p) =>
        Object.assign(p.decision, {
          transport: "tunnel",
          reason: "address_forbidden",
          grant: ref(),
          private_grant: ref(),
        }),
    ],
    [
      "default allow",
      false,
      (p) => {
        p.default = "allow";
        p.decision.allowed = true;
      },
    ],
    [
      "destination block request",
      false,
      (p) =>
        Object.assign(p.decision, {
          reason: "destination_block",
          grant: ref(),
        }),
    ],
    [
      "destination block connect",
      true,
      (p) =>
        Object.assign(p.decision, {
          reason: "destination_block",
          transport: "none",
          grant: ref(),
        }),
    ],
    [
      "request block",
      false,
      (p) =>
        Object.assign(p.decision, { reason: "request_block", grant: ref() }),
    ],
    [
      "request allow",
      false,
      (p) =>
        Object.assign(p.decision, {
          reason: "request_allow",
          allowed: true,
          grant: ref(),
          private_grant: ref(),
        }),
    ],
    [
      "tunnel allow",
      true,
      (p) =>
        Object.assign(p.decision, {
          reason: "tunnel_allow",
          allowed: true,
          transport: "tunnel",
          grant: ref(),
          private_grant: ref(),
        }),
    ],
    [
      "intercept",
      true,
      (p) =>
        Object.assign(p.decision, {
          reason: "intercept_required",
          transport: "intercept",
        }),
    ],
    [
      "credential",
      false,
      (p) =>
        Object.assign(p.decision, {
          reason: "request_allow",
          allowed: true,
          grant: ref(),
          credential: ref(credential),
          credential_grant: ref(),
        }),
    ],
    [
      "unavailable",
      false,
      (p) =>
        Object.assign(p.decision, {
          reason: "credential_unavailable",
          grant: ref(),
          credential: ref(credential),
          credential_grant: ref(),
        }),
    ],
    [
      "conflict",
      false,
      (p) =>
        Object.assign(p.decision, {
          reason: "credential_conflict",
          grant: ref(),
          credential: ref(credential),
          credential_grant: ref(),
          conflict_credential: ref(otherCredential),
          conflict_grant: ref(otherGrant),
        }),
    ],
  ];
  for (const [name, connect, edit] of cases) {
    const p = preview();
    edit(p);
    assert.equal(decodePreview(p, principal, connect), p, name);
  }
});
test("preview rejects malformed defaults instead of coercing them", () => {
  for (const value of [
    null,
    true,
    1,
    "unknown",
    ["allow"],
    ["block"],
    { toString: () => "allow" },
  ]) {
    assert.throws(() =>
      decodePreview({ ...preview(), default: value }, principal, false),
    );
  }
});

test("preview rejects wrong request identity and incoherent or incomplete evidence", () => {
  const edits: Array<(p: ReturnType<typeof preview>) => void> = [
    (p) => {
      p.decision.principal = ref(grantID);
    },
    (p) => {
      p.decision.allowed = true;
    },
    (p) => {
      p.decision.transport = "tunnel";
    },
    (p) => {
      p.decision.reason = "request_allow";
      p.decision.allowed = true;
    },
    (p) => {
      p.decision.reason = "intercept_required";
    },
    (p) => {
      p.decision.credential = ref(credential);
    },
    (p) => {
      p.decision.private_grant = ref();
    },
    (p) => {
      p.decision.grant = null;
    },
    (p) => {
      p.decision.policy_revision = 0;
    },
    (p) => {
      p.decision.default_revision = Number.MAX_SAFE_INTEGER + 1;
    },
    (p) => {
      p.network_verified = true;
    },
    (p) => {
      p.decision.reason = "address_forbidden";
    },
    (p) => {
      p.decision.reason = "private_permission_required";
    },
    (p) => {
      p.decision.unknown = true;
    },
  ];
  for (const [i, edit] of edits.entries()) {
    const p = preview();
    edit(p);
    assert.throws(() => decodePreview(p, principal, false), String(i));
  }
  assert.throws(() => decodePreview(preview(), principal, true));
  for (const key of [
    "principal",
    "grant",
    "private_grant",
    "credential",
    "credential_grant",
    "conflict_credential",
    "conflict_grant",
  ]) {
    const p = preview();
    Object.assign(p.decision, {
      reason: "credential_conflict",
      grant: ref(),
      private_grant: ref(),
      credential: ref(credential),
      credential_grant: ref(),
      conflict_credential: ref(otherCredential),
      conflict_grant: ref(otherGrant),
    });
    assert.equal(decodePreview(p, principal, false), p);
    object(p.decision[key]).revision = 0;
    assert.throws(() => decodePreview(p, principal, false), key);
  }
});
