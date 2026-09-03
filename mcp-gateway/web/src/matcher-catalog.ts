import { decodeDescriptorPage, type DescriptorView } from "./server-reads";
import type { SessionClient } from "./session";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;

export async function readMatcherDescriptors(
  session: SessionClient,
  serverID: string,
): Promise<DescriptorView[] | undefined> {
  if (!gatewayID.test(serverID) || serverID === "00000000000000000000000000")
    return [];
  return session.runProtected(async (context) => {
    const items: DescriptorView[] = [];
    let cursor: string | null = null;
    let restarted = false;
    for (;;) {
      const query = new URLSearchParams({ limit: "100" });
      if (cursor !== null) query.set("cursor", cursor);
      const response = await fetch(
        `/api/v1/servers/${serverID}/descriptors?${query}`,
        {
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        },
      );
      if (await context.sessionLost(response)) return undefined;
      if (response.status === 409 && cursor !== null && !restarted) {
        items.length = 0;
        cursor = null;
        restarted = true;
        continue;
      }
      if (
        !response.ok ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Catalog tools are unavailable.");
      const page = decodeDescriptorPage((await response.json()) as unknown);
      items.push(...page.items.filter((item) => item.retiredAt === null));
      if (page.nextCursor === null) return items;
      cursor = page.nextCursor;
    }
  });
}
