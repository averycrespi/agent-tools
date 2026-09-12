import { MutationCoordinator, type MutationSpec } from "../../src/mutation.ts";
import {
  type OAuthPresenter,
  type OneTimePresenter,
  SensitiveSinkCoordinator,
  copyToClipboard,
  openOAuthWindow,
} from "../../src/sinks.ts";
import {
  SessionClient,
  parseProblem,
  parseSessionBootstrap,
} from "../../src/session.ts";
import {
  ViewCoordinator,
  type VisibilitySource,
  parseInvalidation,
} from "../../src/view.ts";
import { eventually, fail, sessionFixture } from "./shared.ts";

export async function assertSessionFoundationEpochs(): Promise<void> {
  if (
    parseSessionBootstrap(sessionFixture()) === undefined ||
    parseSessionBootstrap({ ...sessionFixture(), extra: "secret" }) !==
      undefined ||
    parseProblem({
      status: 401,
      code: "authentication_required",
      title: "Authentication is required.",
    }) === undefined ||
    parseProblem({
      status: 401,
      code: "authentication_required",
      title: "Authentication is required.",
      extra: "secret",
    }) !== undefined ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
    }) === undefined ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
      context: {
        field: "transport.working_directory",
        rule: "canonical_absolute_path",
      },
    })?.context?.field !== "transport.working_directory" ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
      context: { field: "transport.working_directory", rule: "invented" },
    }) !== undefined
  ) {
    fail("closed session validators changed");
  }

  let bootstrapCalls = 0;
  const request: typeof fetch = async (input, init) => {
    const path = String(input);
    if (path === "/api/v1/admin-sessions/current") {
      bootstrapCalls += 1;
      return new Response(
        JSON.stringify({
          status: 401,
          code: "authentication_required",
          title: "Authentication is required.",
        }),
        {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    }
    if (
      path === "/api/v1/admin-sessions" &&
      init?.method === "POST" &&
      init.headers !== undefined
    ) {
      return new Response(JSON.stringify(sessionFixture()), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(null, { status: 204 });
  };
  const client = new SessionClient(request);
  const lifecycles: string[] = [];
  client.subscribe((snapshot) => lifecycles.push(snapshot.lifecycle));
  client.start();
  for (let index = 0; index < 8; index += 1) await Promise.resolve();
  if (client.snapshot().lifecycle !== "signed_out")
    fail("initial session bootstrap did not settle safely");
  if (!(await client.exchange("mgw_admin_epoch-test-canary")))
    fail("session foundation exchange failed");
  const lostEpoch = client.snapshot().epoch;

  let clearCount = 0;
  client.registerProtectedState(() => {
    clearCount += 1;
  });
  let release: (() => void) | undefined;
  const barrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  let mutationSubmissions = 0;
  let abortObserved = false;
  let timerRan = false;
  client.scheduleProtected(() => {
    timerRan = true;
  }, 0);
  const lateRead = client.runProtected(async ({ signal }) => {
    signal.addEventListener("abort", () => {
      abortObserved = true;
    });
    await barrier;
    return {
      read: "late-read",
      bearer: "mgw_admin_late-bearer",
      oauthURL: "https://secret.invalid/callback",
      event: "late-event",
    };
  });
  const lateMutation = client.runProtected(async () => {
    mutationSubmissions += 1;
    await barrier;
    return "late-mutation";
  });
  const firstRecovery = client.recoverAfterSessionLoss();
  const duplicateRecovery = client.recoverAfterSessionLoss();
  if (firstRecovery !== duplicateRecovery)
    fail("session loss started duplicate bootstrap work");
  release?.();
  const [readResult, mutationResult] = await Promise.all([
    lateRead,
    lateMutation,
  ]);
  await Promise.all([firstRecovery, duplicateRecovery]);
  await client.recoverAfterSessionLoss(lostEpoch);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (
    readResult !== undefined ||
    mutationResult !== undefined ||
    mutationSubmissions !== 1 ||
    bootstrapCalls !== 2 ||
    timerRan ||
    !abortObserved ||
    clearCount !== 1 ||
    !lifecycles.includes("reauthenticating") ||
    client.snapshot().lifecycle !== "signed_out"
  ) {
    fail("authentication epoch did not fence prior work");
  }
}

export async function assertViewGenerationFoundation(): Promise<void> {
  if (
    parseInvalidation({ kind: "system_status", resource_id: null }) ===
      undefined ||
    parseInvalidation({
      kind: "servers",
      resource_id: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    }) === undefined ||
    parseInvalidation({
      kind: "servers",
      resource_id: null,
      authority: "forbidden",
    }) !== undefined
  ) {
    fail("closed invalidation validator changed");
  }

  const sessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return new Response(
        JSON.stringify({
          status: 401,
          code: "authentication_required",
          title: "Authentication is required.",
        }),
        {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(sessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "view session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_view-generation-canary")))
    fail("view session exchange failed");

  let visible = false;
  let visibilityListener = () => {};
  const visibility: VisibilitySource = {
    isVisible: () => visible,
    subscribe: (listener) => {
      visibilityListener = listener;
      return () => {
        visibilityListener = () => {};
      };
    },
  };
  const streamControllers: Array<ReadableStreamDefaultController<Uint8Array>> =
    [];
  let streamRequests = 0;
  const viewRequest: typeof fetch = async (input, init) => {
    if (
      String(input) !== "/api/v1/events" ||
      init?.method !== "POST" ||
      init.body !== "{}"
    ) {
      throw new Error("unexpected view request");
    }
    streamRequests += 1;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        streamControllers.push(controller);
        controller.enqueue(new TextEncoder().encode(": keepalive\n\n"));
      },
    });
    return new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    });
  };
  const coordinator = new ViewCoordinator(session, {
    request: viewRequest,
    visibility,
    reconnectMilliseconds: 20,
  });
  let aCalls = 0;
  let bCalls = 0;
  let publishedA = "";
  let publishedB = "";
  let releaseLate: (() => void) | undefined;
  const late = new Promise<void>((resolve) => {
    releaseLate = resolve;
  });
  let staleReadAborted = false;
  coordinator.registerPanel<string>({
    id: "a",
    matches: () => true,
    invalidations: ["system_status"],
    pollMilliseconds: 40,
    read: async ({ signal }) => {
      aCalls += 1;
      if (aCalls === 2) {
        signal.addEventListener("abort", () => {
          staleReadAborted = true;
        });
        await late;
        return "late";
      }
      return aCalls === 1 ? "initial" : `a-${aCalls}`;
    },
    publish: (value) => {
      publishedA = value;
    },
  });
  coordinator.registerPanel<string>({
    id: "b",
    matches: () => true,
    invalidations: ["backups"],
    pollMilliseconds: 40,
    read: async () => {
      bCalls += 1;
      if (bCalls === 2) throw new Error("isolated panel failure");
      return `b-${bCalls}`;
    },
    publish: (value) => {
      publishedB = value;
    },
  });
  coordinator.activate("#/overview");
  await eventually(
    () =>
      publishedA === "initial" &&
      publishedB === "b-1" &&
      coordinator.snapshot().freshness === "current",
    "initial view snapshot did not become current",
  );

  coordinator.manualRefresh();
  if (coordinator.snapshot().freshness !== "current")
    fail("background refresh exposed transient global staleness");
  await eventually(
    () => coordinator.snapshot().panels.b?.status === "error",
    "panel failure was not isolated",
  );
  if (
    coordinator.snapshot().panels.a?.status !== "current" ||
    coordinator.snapshot().panels.a?.hasValue !== true
  )
    fail("background refresh did not preserve current prior data");
  coordinator.navigate("#/servers");
  if (
    coordinator.snapshot().panels.a?.status !== "loading" ||
    coordinator.snapshot().panels.a?.hasValue !== false
  )
    fail("navigation reused a value owned by another location");
  await eventually(
    () => publishedA === "a-3" && publishedB === "b-3",
    "new view generation did not publish",
  );
  releaseLate?.();
  await Promise.resolve();
  if (publishedA !== "a-3" || !staleReadAborted)
    fail("superseded view read was not aborted and discarded");

  const generationBeforeEvents = coordinator.snapshot().generation;
  const bCallsBeforeEvents = bCalls;
  const eventFrame = new TextEncoder().encode(
    'event: invalidate\ndata: {"kind":"system_status","resource_id":null}\n\n',
  );
  streamControllers[0]?.enqueue(eventFrame);
  streamControllers[0]?.enqueue(eventFrame);
  await eventually(
    () => coordinator.snapshot().generation > generationBeforeEvents,
    "coalesced invalidation did not refresh",
  );
  if (
    coordinator.snapshot().generation !== generationBeforeEvents + 1 ||
    bCalls !== bCallsBeforeEvents
  ) {
    fail("invalidations were not coalesced to their matching visible panel");
  }

  const callsBeforeVisible = aCalls;
  const bCallsBeforeVisible = bCalls;
  visible = true;
  visibilityListener();
  await eventually(
    () => aCalls > callsBeforeVisible && bCalls > bCallsBeforeVisible,
    "equal-interval visible panel polling did not resume as one group",
  );
  visible = false;
  visibilityListener();
  await new Promise((resolve) => setTimeout(resolve, 80));
  const callsWhileHidden = aCalls;
  await new Promise((resolve) => setTimeout(resolve, 100));
  if (aCalls !== callsWhileHidden)
    fail("hidden document polling did not pause");

  const generationBeforeReconnect = coordinator.snapshot().generation;
  streamControllers[0]?.close();
  await eventually(
    () => coordinator.snapshot().freshness === "reconnecting",
    "stream loss was not labeled reconnecting",
  );
  await eventually(
    () =>
      streamRequests === 2 &&
      coordinator.snapshot().freshness === "current" &&
      coordinator.snapshot().generation > generationBeforeReconnect,
    "reconnect did not reload the visible snapshot",
  );
  coordinator.close();
}

export function problemResponse(status: number, code: string): Response {
  return new Response(
    JSON.stringify({ status, code, title: `Safe ${code} response.` }),
    {
      status,
      headers: { "Content-Type": "application/problem+json" },
    },
  );
}

export function mutationSpec(
  overrides: Partial<MutationSpec<string>> = {},
): MutationSpec<string> {
  return {
    route: "/api/v1/servers",
    method: "POST",
    body: '{"namespace":"alpha"}',
    precondition: null,
    requiresPrecondition: false,
    idempotency: "server_create",
    successStatuses: [201],
    decode: async (response) => {
      if (response.headers.get("Content-Type") !== "application/json")
        throw new Error("invalid success type");
      const value = (await response.json()) as unknown;
      if (
        typeof value !== "object" ||
        value === null ||
        !("result" in value) ||
        typeof value.result !== "string"
      ) {
        throw new Error("invalid success body");
      }
      return value.result;
    },
    ...overrides,
  };
}

export async function assertMutationFoundation(): Promise<void> {
  const fakeSessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return problemResponse(401, "authentication_required");
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(fakeSessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "mutation session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_mutation-state-canary")))
    fail("mutation session exchange failed");

  interface ObservedMutation {
    route: string;
    method: string;
    body: string | null;
    precondition: string | null;
    idempotencyKey: string | null;
    csrf: string | null;
  }
  const observed: ObservedMutation[] = [];
  const steps: Array<() => Promise<Response>> = [];
  const request: typeof fetch = async (input, init) => {
    const headers = new Headers(init?.headers);
    observed.push({
      route: String(input),
      method: init?.method ?? "",
      body: typeof init?.body === "string" ? init.body : null,
      precondition: headers.get("If-Match"),
      idempotencyKey: headers.get("Idempotency-Key"),
      csrf: headers.get("X-CSRF-Token"),
    });
    const step = steps.shift();
    if (step === undefined) throw new Error("unexpected mutation request");
    return step();
  };
  let refreshes = 0;
  let keySequence = 0;
  const coordinator = new MutationCoordinator(session, {
    request,
    refreshCurrent: () => {
      refreshes += 1;
    },
    key: () => `test-key-${(keySequence += 1)}`,
  });
  const controller = coordinator.create<string>();

  let releaseFirst: (() => void) | undefined;
  const firstBarrier = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  steps.push(async () => {
    await firstBarrier;
    throw new Error("post-handoff transport loss");
  });
  controller.begin(mutationSpec());
  controller.confirm();
  const initial = controller.submit();
  const duplicate = controller.submit();
  if (initial !== duplicate || controller.snapshot().state !== "submitting")
    fail("duplicate submission was not fenced");
  releaseFirst?.();
  const uncertain = await initial;
  if (
    uncertain.kind !== "uncertain" ||
    !controller.snapshot().canReplay ||
    observed.length !== 1 ||
    observed[0]?.idempotencyKey !== "test-key-1" ||
    observed[0]?.csrf !== "A".repeat(43)
  ) {
    fail("idempotent uncertainty tuple was not retained exactly");
  }
  await Promise.resolve();
  if (observed.length !== 1) fail("uncertain mutation replayed automatically");

  steps.push(
    async () =>
      new Response('{"result":"replayed"}', {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
  );
  const replayed = await controller.replay();
  if (
    replayed.kind !== "acknowledged" ||
    replayed.value !== "replayed" ||
    observed[1]?.idempotencyKey !== "test-key-1" ||
    controller.snapshot().canReplay
  ) {
    fail("explicit same-intent replay changed its tuple");
  }

  steps.push(async () => {
    throw new Error("uncertain first edit");
  });
  controller.begin(mutationSpec({ body: '{"namespace":"bravo"}' }));
  await controller.submit();
  if (observed[2]?.idempotencyKey !== "test-key-2")
    fail("edited idempotent intent did not mint a new tuple");
  steps.push(
    async () =>
      new Response('{"result":"edited"}', {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
  );
  controller.begin(mutationSpec({ body: '{"namespace":"charlie"}' }));
  const edited = await controller.submit();
  if (
    edited.kind !== "acknowledged" ||
    observed[3]?.idempotencyKey !== "test-key-3"
  ) {
    fail("different intent reused an uncertain idempotency tuple");
  }

  const resourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
  const precondition = `"server-${resourceID}-7"`;
  const conditional = mutationSpec({
    route: `/api/v1/servers/${resourceID}`,
    method: "PATCH",
    body: '{"display_name":"updated"}',
    precondition,
    requiresPrecondition: true,
    idempotency: "none",
    successStatuses: [200],
  });
  for (const [status, code, shouldRefresh] of [
    [412, "stale_revision", true],
    [428, "precondition_required", true],
    [409, "conflict", true],
    [429, "resource_limit", false],
    [503, "keyring_unavailable", false],
  ] as const) {
    steps.push(async () => problemResponse(status, code));
    controller.begin(conditional);
    controller.confirm();
    const outcome = await controller.submit();
    if (
      outcome.kind !== "rejected" ||
      outcome.requiresRefresh !== shouldRefresh ||
      controller.snapshot().requiresRefresh !== shouldRefresh ||
      observed.at(-1)?.precondition !== precondition
    ) {
      fail(`conditional mutation classification changed for ${status}`);
    }
  }
  if (refreshes !== 3)
    fail("conflicts did not trigger exact authoritative refreshes");

  steps.push(async () => problemResponse(503, "storage_unavailable"));
  controller.begin(conditional);
  const latched = await controller.submit();
  if (
    latched.kind !== "uncertain" ||
    coordinator.snapshot() !== "storage_latched" ||
    controller.snapshot().availability !== "storage_latched"
  ) {
    fail("storage-latched response did not close global mutation admission");
  }
  const blocked = coordinator.create<string>();
  blocked.begin(conditional);
  const requestCountAtLatch = observed.length;
  if (
    (await blocked.submit()).kind !== "discarded" ||
    observed.length !== requestCountAtLatch
  ) {
    fail("storage latch admitted a new mutation");
  }
  coordinator.setStorageLatched(false);

  steps.push(async () => {
    throw new Error("non-idempotent transport loss");
  });
  blocked.begin(conditional);
  const nonIdempotent = await blocked.submit();
  if (
    nonIdempotent.kind !== "uncertain" ||
    blocked.snapshot().canReplay ||
    (await blocked.replay()).kind !== "discarded"
  ) {
    fail("non-idempotent uncertainty offered replay");
  }

  const invalidResponse = coordinator.create<string>();
  steps.push(
    async () =>
      new Response("not-json", {
        status: 201,
        headers: { "Content-Type": "text/plain" },
      }),
  );
  invalidResponse.begin(mutationSpec());
  const invalid = await invalidResponse.submit();
  if (invalid.kind !== "uncertain" || !invalidResponse.snapshot().canReplay)
    fail("invalid post-handoff success was not uncertain");

  const epochTuple = coordinator.create<string>();
  steps.push(async () => {
    throw new Error("epoch-loss uncertainty");
  });
  epochTuple.begin(mutationSpec());
  await epochTuple.submit();
  if (!epochTuple.snapshot().canReplay)
    fail("epoch tuple setup did not become uncertain");
  await session.recoverAfterSessionLoss();
  if (
    epochTuple.snapshot().state !== "editing" ||
    epochTuple.snapshot().canReplay
  ) {
    fail("authentication epoch loss retained mutation recovery state");
  }

  const requestCountBeforeInvalid = observed.length;
  let invalidSpecRejected = false;
  try {
    controller.begin(
      mutationSpec({
        route: "/api/v1/servers?unsafe=true",
        idempotency: "server_create",
      }),
    );
  } catch {
    invalidSpecRejected = true;
  }
  if (!invalidSpecRejected || observed.length !== requestCountBeforeInvalid)
    fail("invalid mutation reached handoff");

  const routeValidation = coordinator.create<string>();
  routeValidation.begin(
    mutationSpec({
      route: "/api/v1/backups",
      body: "{}",
      idempotency: "backup_create",
    }),
  );
  routeValidation.abandon();
  routeValidation.begin(
    mutationSpec({
      route: `/api/v1/servers/${resourceID}/operations`,
      body: '{"kind":"reload"}',
      precondition,
      requiresPrecondition: true,
      idempotency: "operation_start",
      successStatuses: [200, 202],
    }),
  );
  routeValidation.abandon();
  let missingMechanicsRejected = false;
  try {
    routeValidation.begin(
      mutationSpec({
        route: `/api/v1/servers/${resourceID}/operations`,
        body: '{"kind":"reload"}',
        idempotency: "none",
      }),
    );
  } catch {
    missingMechanicsRejected = true;
  }
  if (!missingMechanicsRejected)
    fail("route-specific idempotency and precondition mechanics were optional");
  coordinator.close();
}

export async function assertSensitiveSinkFoundation(): Promise<void> {
  const fakeSessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return problemResponse(401, "authentication_required");
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(fakeSessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "sink session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_sink-foundation-canary")))
    fail("sink session exchange failed");

  const copiedValues: string[] = [];
  if (
    (await copyToClipboard("copy-canary", async (value) => {
      copiedValues.push(value);
    })) !== "copied" ||
    copiedValues.join("") !== "copy-canary" ||
    (await copyToClipboard("failure-canary", async () => {
      throw new Error("clipboard denied");
    })) !== "failed"
  ) {
    fail("clipboard success and failure were not classified safely");
  }
  const popup = { opener: "retained" } as unknown as WindowProxy;
  const openArguments: string[] = [];
  if (
    openOAuthWindow(
      "https://auth.example/authorize",
      (target, name, features) => {
        openArguments.push(target, name, features);
        return popup;
      },
    ) !== "opened" ||
    openArguments.join("|") !==
      "https://auth.example/authorize|_blank|noopener,noreferrer" ||
    popup.opener !== null ||
    openOAuthWindow("https://auth.example/authorize", () => null) !== "blocked"
  ) {
    fail("OAuth opener did not enforce its closed user-gesture mechanics");
  }

  const coordinator = new SensitiveSinkCoordinator(session);
  if (coordinator.prepareOneTime("Unavailable display") !== undefined)
    fail("secret-bearing mutation admitted without a prepared presenter");

  let displayedSecret = "";
  let oneTimeGeneration = 0;
  let oneTimeLost = false;
  let oneTimeClears = 0;
  const oneTimePresenter: OneTimePresenter = {
    prepare: (_label, generation) => {
      oneTimeGeneration = generation;
      oneTimeLost = false;
      return true;
    },
    publish: (value, generation) => {
      if (generation !== oneTimeGeneration) return false;
      displayedSecret = value;
      return true;
    },
    lose: (generation) => {
      if (generation !== oneTimeGeneration) return;
      displayedSecret = "";
      oneTimeLost = true;
    },
    clear: () => {
      displayedSecret = "";
      oneTimeLost = false;
      oneTimeClears += 1;
    },
  };
  coordinator.registerOneTimePresenter(oneTimePresenter);
  const bearerCanary = `mgw_admin_${"B".repeat(43)}`;
  const prepared = coordinator.prepareOneTime("New administrator bearer");
  if (prepared === undefined || displayedSecret !== "")
    fail("one-time display was not pre-created while blank");
  if (
    prepared.publish(bearerCanary) !== "published" ||
    displayedSecret !== bearerCanary
  )
    fail("prepared one-time display did not receive the exact bearer");
  coordinator.dismiss(oneTimeGeneration);
  if (displayedSecret !== "" || oneTimeClears === 0)
    fail("one-time dismissal retained its string");

  const uncertain = coordinator.prepareOneTime("Uncertain bearer response");
  if (uncertain === undefined) fail("uncertain sink setup failed");
  uncertain.lose();
  if (!oneTimeLost || displayedSecret !== "")
    fail("lost one-time response retained or echoed a value");
  coordinator.dismiss(oneTimeGeneration);

  const navigated = coordinator.prepareOneTime("Navigation fence");
  if (navigated === undefined) fail("navigation sink setup failed");
  coordinator.clearForNavigation();
  if (navigated.publish(bearerCanary) !== "lost" || displayedSecret !== "")
    fail("navigation accepted a late one-time value");

  const writeOnly = coordinator.createWriteOnly();
  const input = { value: "" } as HTMLInputElement;
  writeOnly.attach(input);
  input.value = "write-only-canary";
  if (writeOnly.read() !== "write-only-canary")
    fail("write-only field did not expose its live submission value");
  coordinator.clearForNavigation();
  if (input.value !== "") fail("navigation retained a write-only value");

  let oauthURL = "";
  const currentOAuthURL = () => oauthURL;
  let oauthGeneration = 0;
  let oauthLost = false;
  const oauthPresenter: OAuthPresenter = {
    prepare: (_label, generation) => {
      oauthGeneration = generation;
      oauthLost = false;
      return true;
    },
    publish: (value, generation) => {
      if (generation !== oauthGeneration) return false;
      oauthURL = value;
      return true;
    },
    lose: (generation) => {
      if (generation !== oauthGeneration) return;
      oauthURL = "";
      oauthLost = true;
    },
    clear: () => {
      oauthURL = "";
      oauthLost = false;
    },
  };
  coordinator.registerOAuthPresenter(oauthPresenter);
  const oauth = coordinator.prepareOAuth("Authorize local server");
  const validURL =
    "https://auth.example/authorize?client_id=public&state=opaque";
  if (
    oauth === undefined ||
    oauth.publish(validURL) !== "published" ||
    oauthURL !== validURL
  )
    fail("prepared OAuth display rejected a canonical URL");
  coordinator.dismiss(oauthGeneration);
  const invalidOAuth = coordinator.prepareOAuth("Reject active URL");
  if (
    invalidOAuth === undefined ||
    invalidOAuth.publish("javascript:alert(1)") !== "lost" ||
    !oauthLost ||
    currentOAuthURL() !== ""
  ) {
    fail("OAuth sink accepted an active or invalid URL");
  }
  coordinator.dismiss(oauthGeneration);

  const epoch = coordinator.prepareOneTime("Epoch fence");
  if (epoch === undefined) fail("epoch sink setup failed");
  await session.recoverAfterSessionLoss();
  if (
    epoch.publish(bearerCanary) !== "lost" ||
    displayedSecret !== "" ||
    input.value !== ""
  )
    fail("authentication epoch loss retained sensitive sink state");
  writeOnly.close();
  coordinator.close();
}
