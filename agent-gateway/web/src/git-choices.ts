import { useEffect, useState } from "preact/hooks";
import { decodePrincipal } from "./principals";
import { decodeGitResource, type GitResource } from "./git-contract";
import type { SessionClient } from "./session";
import { readCollectionPage, type CollectionPage } from "./view";

export interface NamedChoice {
  id: string;
  name: string;
}
export interface GitChoices {
  agents: NamedChoice[];
  repositories: GitResource[];
  credentials: GitResource[];
  loading: boolean;
  error: boolean;
}

// Traverse the existing snapshot collection owner, bounded by resource inventory limits.
export async function readGitChoices<T extends { id: string }>(
  session: SessionClient,
  path: string,
  decode: (value: unknown) => T,
  maximum: number,
  signal: AbortSignal,
): Promise<T[]> {
  const items: T[] = [];
  let cursor: string | null = null;
  for (let page = 0; page < Math.ceil(maximum / 50); page++) {
    const result: CollectionPage<T> | undefined = await readCollectionPage(
      session,
      `${path}?limit=50${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
      decode,
      signal,
    );
    if (
      !result ||
      result.totalCount === undefined ||
      result.totalCount > maximum ||
      result.offset !== items.length
    )
      throw new Error("Choices unavailable");
    items.push(...result.items);
    if (
      items.length > maximum ||
      new Set(items.map((item) => item.id)).size !== items.length
    )
      throw new Error("Invalid choices");
    if (result.nextCursor === null) return items;
    cursor = result.nextCursor;
  }
  throw new Error("Choice limit exceeded");
}

export function useGitChoices(
  session: SessionClient,
  generation: number,
  kind: "repositories" | "grants" | "credentials" | "traffic",
): GitChoices {
  const [state, setState] = useState<GitChoices & { generation: number }>({
    generation: -1,
    agents: [],
    repositories: [],
    credentials: [],
    loading: true,
    error: false,
  });
  useEffect(() => {
    const abort = new AbortController();
    setState((previous) => ({ ...previous, loading: true }));
    void Promise.all([
      kind === "grants"
        ? readGitChoices(
            session,
            "/api/v2/principals",
            (value) => {
              const p = decodePrincipal(value);
              return { id: p.id, name: p.displayName };
            },
            128,
            abort.signal,
          )
        : [],
      kind !== "repositories"
        ? readGitChoices(
            session,
            "/api/v2/git/repositories",
            (value) => decodeGitResource("repositories", value),
            256,
            abort.signal,
          )
        : [],
      kind === "repositories"
        ? readGitChoices(
            session,
            "/api/v2/git/credentials",
            (value) => decodeGitResource("credentials", value),
            256,
            abort.signal,
          )
        : [],
    ])
      .then(([agents, repositories, credentials]) => {
        if (!abort.signal.aborted)
          setState({
            generation,
            agents,
            repositories,
            credentials,
            loading: false,
            error: false,
          });
      })
      .catch(() => {
        if (!abort.signal.aborted)
          setState((previous) => ({
            ...previous,
            generation,
            loading: false,
            error: true,
          }));
      });
    return () => abort.abort();
  }, [session, generation, kind]);
  return state.generation === generation ? state : { ...state, loading: true };
}

export function choiceLabel(
  choice: { id: string; name?: string },
  choices: readonly { id: string; name?: string }[],
): string {
  return `${choice.name || "Unnamed resource"}${choices.filter((item) => item.name === choice.name).length > 1 ? ` · ${choice.id}` : ""}`;
}

export function compatibleGitCredential(
  credential: GitResource,
  destination: string,
): boolean {
  try {
    return (
      credential.available === true &&
      credential.origin !== undefined &&
      new URL(destination).origin === new URL(credential.origin).origin
    );
  } catch {
    return false;
  }
}
