import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { MutationCoordinator, MutationSnapshot } from "./mutation";
import { useUnsavedChanges } from "./navigation";
import {
  ConfirmationDialog,
  DetailComparison,
  FormField,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { SessionClient } from "./session";
import type { ViewSnapshot } from "./view";
import {
  decodeGitRoutingResponse,
  gitRoutingETag,
  normalizeGitOrigins,
  type GitRoutingProfile,
} from "./git-routing-contract";

const route = "/api/v2/git/routing-profile";
interface Props {
  session: SessionClient;
  mutations: MutationCoordinator;
  view: ViewSnapshot;
  onRefresh: () => void;
}
function Origins({ values }: { values: string[] }) {
  return values.length ? (
    <ul>
      {values.map((origin) => (
        <li key={origin} class="technical-value">
          {origin}
        </li>
      ))}
    </ul>
  ) : (
    <span>No origins enabled</span>
  );
}
export function GitRouting(props: Props) {
  const [profile, setProfile] = useState<GitRoutingProfile>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const readGeneration = useRef(0);
  useEffect(() => {
    const generation = ++readGeneration.current;
    setLoading(true);
    void props.session
      .runProtected(async (context) => {
        const response = await fetch(route, {
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok) throw new Error("Read unavailable");
        const value = await decodeGitRoutingResponse(response);
        if (generation === readGeneration.current) {
          setProfile(value);
          setError(false);
        }
      })
      .catch(() => {
        if (generation === readGeneration.current) setError(true);
      })
      .finally(() => {
        if (generation === readGeneration.current) setLoading(false);
      });
    return () => {
      readGeneration.current++;
    };
  }, [props.session, props.view.generation]);
  if (!profile)
    return (
      <StateNotice
        state={error ? "error" : "loading"}
        title={error ? "Git routing unavailable" : "Loading Git routing"}
      >
        {error && "Refresh to load the routing profile."}
      </StateNotice>
    );
  return (
    <div class="domain-view">
      {error && (
        <StateNotice state="error" title="Git routing refresh unavailable">
          Saved facts may be stale. Refresh before making changes.
        </StateNotice>
      )}
      <section class="detail-section" aria-labelledby="git-routing-details">
        <h2 id="git-routing-details">Routing profile</h2>
        <dl class="detail-facts">
          <div>
            <dt>Enforcement</dt>
            <dd>
              <StatusLabel
                state={error ? "stale" : profile.active ? "current" : "neutral"}
              >
                {error ? "Last known: " : ""}
                {profile.active ? "Active" : "Inactive"}
              </StatusLabel>
            </dd>
          </div>
          <div>
            <dt>Enabled origins</dt>
            <dd>
              <Origins values={profile.origins} />
            </dd>
          </div>
          <div>
            <dt>Revision</dt>
            <dd>{profile.revision}</dd>
          </div>
        </dl>
      </section>
      <RoutingEditor
        {...props}
        profile={profile}
        unavailable={loading || error}
        onSaved={(value) => {
          // An older read must not overwrite an acknowledged mutation.
          readGeneration.current++;
          setProfile(value);
          setError(false);
          props.onRefresh();
        }}
      />
    </div>
  );
}
function RoutingEditor({
  profile,
  unavailable,
  onSaved,
  ...props
}: Props & {
  profile: GitRoutingProfile;
  unavailable: boolean;
  onSaved: (value: GitRoutingProfile) => void;
}) {
  const [draft, setDraft] = useState(profile.origins);
  const [expected, setExpected] = useState(gitRoutingETag(profile));
  const [dirty, setDirty] = useState(false);
  const [reviewed, setReviewed] = useState<string[]>();
  const [inputError, setInputError] = useState<string>();
  const [saved, setSaved] = useState(false);
  const [controller] = useState(() =>
    props.mutations.create<GitRoutingProfile>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const button = useRef<HTMLButtonElement>(null);
  useUnsavedChanges(dirty);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (!dirty && mutation.state === "editing") {
      setDraft(profile.origins);
      setExpected(gitRoutingETag(profile));
    }
  }, [profile, dirty, mutation.state]);
  const changedRevision = expected !== gitRoutingETag(profile);
  const needsReview = changedRevision || mutation.requiresRefresh;
  const locked =
    mutation.state === "submitting" || mutation.state === "uncertain";
  const blocked =
    unavailable ||
    needsReview ||
    locked ||
    mutation.availability === "storage_latched";
  const change = (values: string[]) => {
    setDraft(values);
    setDirty(true);
    setSaved(false);
    setInputError(undefined);
  };
  const review = () => {
    if (blocked) return;
    try {
      setReviewed(normalizeGitOrigins(draft));
      setInputError(undefined);
    } catch (error) {
      setInputError(
        error instanceof Error ? error.message : "Check HTTPS origins.",
      );
    }
  };
  const submit = () => {
    const origins = reviewed;
    setReviewed(undefined);
    if (!origins || blocked) return;
    try {
      controller.begin({
        route,
        method: "PATCH",
        body: JSON.stringify({ origins }),
        precondition: expected,
        requiresPrecondition: true,
        idempotency: "none",
        successStatuses: [200],
        decode: decodeGitRoutingResponse,
      });
      void controller.submit().then((outcome) => {
        if (outcome.kind === "acknowledged") {
          setDraft(outcome.value.origins);
          setExpected(gitRoutingETag(outcome.value));
          setDirty(false);
          setSaved(true);
          controller.abandon();
          onSaved(outcome.value);
        } else if (
          outcome.kind === "uncertain" ||
          (outcome.kind === "rejected" && outcome.requiresRefresh)
        )
          props.onRefresh();
      });
    } catch {
      setInputError(
        "Refresh and review the current routing profile before making a new decision.",
      );
    }
  };
  return (
    <section class="panel domain-panel">
      <div class="panel-heading">
        <h2>Edit routing</h2>
      </div>
      <p>
        Enabled origins use Git repository permissions and credentials for smart
        HTTPS Git requests. Other origins remain under HTTP policy.
      </p>
      {saved && <p role="status">Routing saved</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          review();
        }}
      >
        <fieldset class="git-mutation-fields" disabled={locked}>
          <div class="git-list-editor">
            <h3>HTTPS origins</h3>
            {draft.length === 0 && <p>No origins enabled</p>}
            {draft.map((origin, i) => (
              <div class="form-row" key={i}>
                <FormField
                  id={`git-routing-origin-${i}`}
                  label={`Origin ${i + 1}`}
                >
                  {(attributes) => (
                    <input
                      {...attributes}
                      required
                      value={origin}
                      placeholder="https://github.com"
                      onInput={(e) =>
                        change(
                          draft.map((v, n) =>
                            n === i ? e.currentTarget.value : v,
                          ),
                        )
                      }
                    />
                  )}
                </FormField>
                <button
                  type="button"
                  aria-label={`Remove origin ${i + 1}`}
                  onClick={() => change(draft.filter((_, n) => n !== i))}
                >
                  Remove
                </button>
              </div>
            ))}
            <button
              type="button"
              disabled={draft.length >= 256}
              onClick={() => change([...draft, ""])}
            >
              Add origin
            </button>
          </div>
          {inputError && (
            <StateNotice state="error" title="Check HTTPS origins">
              {inputError}
            </StateNotice>
          )}
          {needsReview && (
            <StateNotice state="warning" title="Review current revision">
              <p>
                Compare the saved origins above with your retained draft before
                continuing.
              </p>
              <button
                type="button"
                disabled={unavailable || locked}
                onClick={() => {
                  setExpected(gitRoutingETag(profile));
                  controller.abandon();
                }}
              >
                Use reviewed revision
              </button>
            </StateNotice>
          )}
          {mutation.problem && (
            <StateNotice state="error" title={mutation.problem.title} />
          )}
          {mutation.state === "uncertain" && (
            <StateNotice state="warning" title="Change outcome unknown">
              Inspect current routing before deciding on another operation. No
              automatic retry.
            </StateNotice>
          )}
          {mutation.availability === "storage_latched" && (
            <StateNotice state="warning" title="Changes unavailable">
              Gateway storage is latched.
            </StateNotice>
          )}
          <div class="form-actions">
            <button
              ref={button}
              type="submit"
              class="form-submit-action"
              disabled={blocked || !dirty}
            >
              Review changes
            </button>
          </div>
        </fieldset>
      </form>
      <ConfirmationDialog
        id="git-routing-confirm"
        open={reviewed !== undefined}
        title="Update Git routing"
        confirmLabel="Save routing"
        returnFocus={button as unknown as RefObject<HTMLElement>}
        onCancel={() => setReviewed(undefined)}
        onConfirm={submit}
        consequence={
          <>
            <DetailComparison
              label="Routing changes"
              beforeLabel="Current"
              afterLabel="Proposed"
              rows={[
                {
                  label: "Enabled origins",
                  before: <Origins values={profile.origins} />,
                  after: <Origins values={reviewed ?? []} />,
                  changed:
                    JSON.stringify(profile.origins) !==
                    JSON.stringify(reviewed),
                },
              ]}
            />
            <p>
              This replaces the enabled-origin list for subsequent requests.
              Enabled origins refuse opaque tunnels; repository grants are still
              required. Removing an origin returns it to HTTP policy.
            </p>
            {blocked && (
              <StateNotice
                state="warning"
                title="Refresh and review before saving"
              />
            )}
          </>
        }
      />
    </section>
  );
}
