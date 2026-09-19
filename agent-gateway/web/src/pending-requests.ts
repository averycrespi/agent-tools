import type { SessionClient } from "./session.ts";
import {
  readCollectionPage,
  type ViewCoordinator,
  type ViewSnapshot,
} from "./view.ts";

const panelID = "sidebar-pending-requests";

export class PendingRequestsController {
  private total: number | undefined;

  constructor(session: SessionClient, views: ViewCoordinator) {
    session.registerProtectedState(() => {
      this.total = undefined;
    });
    views.registerPanel({
      id: panelID,
      scope: "session",
      matches: (key) => key !== "#/sign-in",
      invalidations: ["grant_requests"],
      pollMilliseconds: 30_000,
      read: async ({ signal }) => {
        const page = await readCollectionPage(
          session,
          "/api/v2/mcp/grant-requests?state=pending&limit=1",
          () => null,
          signal,
        );
        if (page?.totalCount === undefined)
          throw new Error("Pending request count is unavailable.");
        return page.totalCount;
      },
      publish: (total) => {
        this.total = total;
      },
    });
  }

  presentation(view: ViewSnapshot): { label: string; badge: string | null } {
    const panel = view.panels[panelID];
    if (this.total === undefined || panel?.hasValue !== true)
      return {
        label:
          panel?.status === "error"
            ? "Requests, pending count unavailable"
            : "Requests, pending count loading",
        badge: "?",
      };
    if (panel.status === "error" || panel.status === "stale")
      return {
        label: `Requests, pending count unavailable; last known ${this.total} pending`,
        badge: "?",
      };
    return {
      label: `Requests, ${this.total} pending${panel.refreshing ? " (refreshing)" : ""}`,
      badge: this.total === 0 ? null : String(this.total),
    };
  }
}
