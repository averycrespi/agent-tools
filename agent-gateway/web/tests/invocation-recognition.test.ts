import assert from "node:assert/strict";
import test from "node:test";
import { decodeInvocationRecognition as decode } from "../src/invocation-recognition.ts";

const serverID = "01J60000000000000000000001";
const grantID = "01J60000000000000000000002";
const metadata = {
  server: { server_id: serverID, display_name: "Current server" },
  grant: { grant_id: grantID, description: "Current grant" },
};
test("invocation recognition is optional, current and bounded separately from evidence", () => {
  assert.equal(decode(undefined, serverID, grantID), undefined);
  assert.deepEqual(decode({ server: null, grant: null }, serverID, grantID), {
    server: null,
    grant: null,
  });
  assert.deepEqual(decode(metadata, serverID, grantID), {
    server: { serverID, displayName: "Current server" },
    grant: { grantID, description: "Current grant" },
  });
  assert.equal(
    decode(
      { ...metadata, grant: { grant_id: grantID, description: "" } },
      serverID,
      grantID,
    )?.grant?.description,
    "",
  );
  assert.equal(
    decode(
      { ...metadata, server: { server_id: serverID, display_name: "Renamed" } },
      serverID,
      grantID,
    )?.server?.displayName,
    "Renamed",
  );
  assert.equal(
    decode(
      {
        ...metadata,
        server: { server_id: serverID, display_name: "é".repeat(128) },
      },
      serverID,
      grantID,
    )?.server?.displayName.length,
    128,
  );
});
test("recognition rejects extra fields, wrong identities and fabricated relationships", () => {
  for (const value of [
    null,
    [],
    {},
    { ...metadata, extra: true },
    { ...metadata, server: { ...metadata.server, server_id: grantID } },
    { ...metadata, grant: { ...metadata.grant, grant_id: serverID } },
    { ...metadata, server: { ...metadata.server, display_name: "" } },
    {
      ...metadata,
      server: { ...metadata.server, display_name: "é".repeat(129) },
    },
    { ...metadata, grant: { ...metadata.grant, description: null } },
    { ...metadata, grant: { ...metadata.grant, description: "x".repeat(257) } },
    { ...metadata, grant: { ...metadata.grant, token: "not allowed" } },
  ])
    assert.throws(() => decode(value, serverID, grantID));
  assert.throws(() => decode(metadata, undefined, grantID));
  assert.throws(() => decode(metadata, serverID, undefined));
  assert.throws(() =>
    decode(
      { server: { server_id: "bad", display_name: "Bad" }, grant: null },
      "bad",
      undefined,
    ),
  );
});
