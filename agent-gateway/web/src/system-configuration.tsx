import { useEffect, useRef, useState } from "preact/hooks";
import {
  bundleDefinitions,
  decodeBundleImport,
  decodeBundlePreview,
} from "./bundle-contract";
import {
  type Creation,
  type DefinitionIdentity,
  type ImportResult,
  resourceDestinations,
  configurationKindLabels,
  configurationRequirementLabel,
} from "./config-contract";
import {
  boundedText,
  ConfigActions,
  ConfigurationInput,
} from "./configuration";
import type { SessionClient } from "./session";
import type { MutationCoordinator } from "./mutation";
import { ConfirmationDialog, StateNotice } from "./primitives";
import { useUnsavedChanges } from "./navigation";

export function SystemConfiguration({
  session,
  mutations,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
}) {
  const [raw, setRaw] = useState("");
  const [error, setError] = useState<string>();
  const [preview, setPreview] = useState<Creation[]>();
  const [result, setResult] = useState<ImportResult>();
  const [checking, setChecking] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [controller] = useState(() => mutations.create<ImportResult>());
  const [mutation, setMutation] = useState(controller.snapshot());
  const generation = useRef(0);
  const button = useRef<HTMLButtonElement>(null);
  useUnsavedChanges(raw !== "" && !result);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(
    () => () => {
      generation.current++;
      controller.close();
    },
    [controller],
  );
  const locked =
    checking ||
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    !!result;
  const change = (value: string) => {
    generation.current++;
    setRaw(value);
    setPreview(undefined);
    setError(undefined);
  };
  const validate = async () => {
    const owned = ++generation.current;
    setPreview(undefined);
    setError(undefined);
    let definitions: DefinitionIdentity[];
    try {
      definitions = bundleDefinitions(raw);
    } catch (e) {
      setError((e as Error).message);
      return;
    }
    setChecking(true);
    try {
      await session.runProtected(async (context) => {
        const response = await fetch("/api/v2/config/preview", {
          method: "POST",
          body: raw,
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok)
          throw new Error(
            response.status === 409
              ? "A key, permanent reservation, domain or dependency conflicts. Check the target collections and edit the bundle, then validate again. Git routing requires an untouched fresh seed."
              : "Validation failed. Check version 2 definitions, explicit typed keys, references and target capacity, then validate again.",
          );
        const value = decodeBundlePreview(
          JSON.parse(await boundedText(response)),
          definitions,
        );
        if (context.isCurrent() && owned === generation.current)
          setPreview(value);
      });
    } catch (e) {
      if (owned === generation.current)
        setError(
          (e instanceof Error && e.message.startsWith("A key,")) ||
            (e instanceof Error && e.message.startsWith("Validation failed."))
            ? e.message
            : "Preview unavailable. Check the connection and validate again.",
        );
    } finally {
      if (owned === generation.current) setChecking(false);
    }
  };
  const submit = async () => {
    if (!preview) return;
    setConfirm(false);
    setError(undefined);
    const definitions = bundleDefinitions(raw);
    controller.begin({
      route: "/api/v2/config/import",
      method: "POST",
      body: raw,
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [201],
      decode: async (response) =>
        decodeBundleImport(
          JSON.parse(await boundedText(response)),
          definitions,
        ),
    });
    controller.confirm();
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") setResult(outcome.value);
    else if (outcome.kind === "rejected") {
      setPreview(undefined);
      setError(
        outcome.problem.status === 409
          ? "Creation rejected: a key, reservation, domain or dependency changed since preview. Nothing was imported. Inspect the target, edit the retained bundle and validate again."
          : "Creation rejected. Nothing was imported. Check definitions and target capacity, then validate again; your input is preserved.",
      );
    }
  };
  const submitted = new Set(preview?.map((v) => `${v.kind}/${v.key ?? ""}`));
  return (
    <>
      <ConfigActions session={session} />
      <section class="panel domain-panel" aria-labelledby="system-config-title">
        <h2 id="system-config-title">Import configuration</h2>
        <p>
          Reusable creation, not updates or file synchronization. Initialize and
          start the target Gateway, then import. Existing and permanently
          reserved keys cannot be reused.
        </p>
        {result ? (
          <StateNotice state="neutral" title="Configuration creation committed">
            <p>
              {result.creations.length} definitions committed. Do not import
              this bundle again.
            </p>
            <p>
              {result.activation === "pending"
                ? "Activation pending. Enabled resources are retained even if startup fails; creation does not establish upstream readiness."
                : "No activation required. Configuration is separate from credential material and readiness."}
            </p>
            <ol class="config-creation-list">
              {result.creations.map((v, index) => (
                <li key={`${v.kind}/${v.key ?? ""}`}>
                  <strong>
                    {configurationKindLabels[v.kind]}
                    {v.key ? ` / ${v.key}` : ""}
                  </strong>
                  {" — "}
                  {v.reserved ? (
                    "Permanent deleted-key reservation"
                  ) : v.kind === "git_routing" ? (
                    <a href="#/git/routing">Routing seed initialized</a>
                  ) : (
                    <a
                      href={`#/${resourceDestinations[v.kind]}/${v.id}${v.kind === "mcp_server" ? "?tab=status" : ""}`}
                    >
                      View created resource
                    </a>
                  )}
                  {preview?.[index]?.requirements.some(
                    (r) =>
                      r !== "activate_after_commit" &&
                      r !== "initialize_untouched_routing_seed",
                  ) && (
                    <p>
                      Required next:{" "}
                      {preview[index]!.requirements.filter(
                        (r) =>
                          r !== "activate_after_commit" &&
                          r !== "initialize_untouched_routing_seed",
                      )
                        .map(configurationRequirementLabel)
                        .join(", ")}
                      .
                    </p>
                  )}
                </li>
              ))}
            </ol>
          </StateNotice>
        ) : (
          <>
            <ConfigurationInput
              raw={raw}
              disabled={locked}
              bundle
              onChange={change}
              onError={setError}
              onReading={setChecking}
            />
            {error && (
              <StateNotice state="error" title="Import not completed">
                <p>{error}</p>
              </StateNotice>
            )}
            {checking && (
              <StateNotice state="loading" title="Validating configuration" />
            )}
            {mutation.state === "submitting" && (
              <StateNotice state="loading" title="Creating configuration" />
            )}
            {mutation.state === "uncertain" && (
              <StateNotice state="warning" title="Creation outcome unknown">
                <p>
                  Inspect every immutable key in the target collections before
                  taking further action. This request will not be replayed; do
                  not import it again to discover the outcome.
                </p>
              </StateNotice>
            )}
            <div class="form-actions">
              <button
                type="button"
                class={preview ? "secondary" : "primary-action"}
                disabled={locked || !raw}
                onClick={() => void validate()}
              >
                Validate and preview
              </button>
            </div>
            {preview && (
              <section class="detail-group" aria-label="Import preview">
                <h3>Creation preview</h3>
                <p>
                  {preview.length} definitions. Every creation is listed below.
                </p>
                <ol class="config-creation-list">
                  {preview.map((v) => (
                    <li key={`${v.kind}/${v.key ?? ""}`}>
                      <h4>
                        {configurationKindLabels[v.kind]}
                        {v.key ? ` / ${v.key}` : ""}
                      </h4>
                      <dl class="detail-facts">
                        <div>
                          <dt>Action</dt>
                          <dd>
                            {v.reserved
                              ? "Reserve deleted key permanently"
                              : v.kind === "git_routing"
                                ? "Initialize untouched routing seed"
                                : "Create resource"}
                          </dd>
                        </div>
                        <div>
                          <dt>Dependencies</dt>
                          <dd>
                            {v.dependencies.length ? (
                              <ul>
                                {v.dependencies.map((ref) => (
                                  <li key={`${ref.kind}/${ref.key ?? ""}`}>
                                    {ref.kind}
                                    {ref.key ? ` / ${ref.key}` : ""}
                                    {" — "}
                                    {ref.kind === "gateway_mcp"
                                      ? "Existing Gateway self-service"
                                      : submitted.has(
                                            `${ref.kind}/${ref.key ?? ""}`,
                                          )
                                        ? "Same submission"
                                        : "Existing target definition"}
                                  </li>
                                ))}
                              </ul>
                            ) : (
                              "None"
                            )}
                          </dd>
                        </div>
                        <div>
                          <dt>Setup requirements</dt>
                          <dd>
                            {v.requirements
                              .map(configurationRequirementLabel)
                              .join(", ") || "None"}
                          </dd>
                        </div>
                      </dl>
                    </li>
                  ))}
                </ol>
                <details>
                  <summary>Exact configuration JSON</summary>
                  <pre class="inert-json">{raw}</pre>
                </details>
                <p>
                  Preview is advisory. Confirmation revalidates the entire
                  bundle atomically. No implicit dependencies, grants,
                  credential values or expiry renewal.
                </p>
                <div class="form-actions">
                  <button
                    type="button"
                    class="primary-action"
                    ref={button}
                    disabled={
                      locked ||
                      !preview.length ||
                      mutation.availability !== "enabled"
                    }
                    onClick={() => setConfirm(true)}
                  >
                    Import
                  </button>
                </div>
              </section>
            )}
          </>
        )}
        <ConfirmationDialog
          id="system-config-confirm"
          open={confirm}
          title="Import configuration?"
          consequence={`Create all ${preview?.length ?? 0} definitions with their exact immutable keys. Existing resources are never updated. Credential material must be provisioned separately; enabled servers activate after commit.`}
          confirmLabel="Confirm import"
          returnFocus={button}
          onCancel={() => setConfirm(false)}
          onConfirm={() => void submit()}
        />
      </section>
    </>
  );
}
