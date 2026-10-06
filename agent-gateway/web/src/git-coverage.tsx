import { useEffect, useState } from "preact/hooks";
import type { SessionClient } from "./session";
import { StatusLabel } from "./primitives";
import {
  decodeGitRoutingResponse,
  gitOriginCovered,
  type GitRoutingProfile,
} from "./git-routing-contract";

export interface GitCoverageSource {
  profile?: GitRoutingProfile;
  loading: boolean;
  error: boolean;
}

// One authorized profile read per repository view, shared by rows and editors.
export function useGitCoverage(
  session: SessionClient,
  generation: number,
  enabled: boolean,
): GitCoverageSource {
  const [state, setState] = useState<
    GitCoverageSource & { generation: number }
  >({
    generation: -1,
    loading: true,
    error: false,
  });
  useEffect(() => {
    if (!enabled) return;
    let current = true;
    void session
      .runProtected(async (context) => {
        const response = await fetch("/api/v2/git/routing-profile", {
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok) throw new Error("Routing unavailable");
        const profile = await decodeGitRoutingResponse(response);
        if (current)
          setState({ generation, profile, loading: false, error: false });
      })
      .catch(() => {
        if (current)
          setState((previous) => ({
            ...previous,
            generation,
            loading: false,
            error: true,
          }));
      });
    return () => {
      current = false;
    };
  }, [session, generation, enabled]);
  return enabled && state.generation === generation
    ? state
    : { ...state, loading: true };
}

export function GitCoverage({
  source,
  destination,
  contextual = false,
}: {
  source: GitCoverageSource;
  destination: string;
  contextual?: boolean;
}) {
  let status: "loading" | "stale" | "warning" | "neutral" = "neutral";
  let text: string;
  if (source.loading) {
    status = "loading";
    text = "Checking routing";
  } else if (source.error || !source.profile) {
    status = source.profile ? "stale" : "warning";
    text = source.profile ? "Routing stale" : "Routing unknown";
  } else if (!source.profile.active) text = "Routing inactive";
  else {
    const coverage = gitOriginCovered(destination, source.profile);
    if (coverage === undefined) {
      status = "warning";
      text = "Routing unknown";
    } else if (coverage) {
      if (!contextual) return null;
      text = "Origin enabled";
    } else {
      status = "warning";
      text = "Not routed";
    }
  }
  return (
    <div class="git-routing-coverage">
      <StatusLabel state={status}>{text}</StatusLabel>
      {contextual && (
        <p>
          {text === "Not routed"
            ? "Repository origin is not enabled in Git routing. "
            : ""}
          <a href="#/git/routing">Git routing</a>
          {
            " · Coverage does not establish grants, credential readiness or access."
          }
        </p>
      )}
    </div>
  );
}
