import { type Browser, chromium, firefox, webkit } from "@playwright/test";
import { createInterface } from "node:readline";
import { fail, loadShell } from "./browser/shared.ts";
import { isAbsolute } from "node:path";
import {
  runAccessManagementReadCanary,
  runGrantCorrection,
  runGrantReadsCreate,
  runPrincipalCredentials,
  runPrincipals,
  runRequestAdjudication,
  runRequestReads,
} from "./browser/access-scenarios.ts";
import {
  runAccessibilityKeyboardResponsive,
  runSecretSinks,
  runSecretStoragePrivacy,
  runVisualAccessibilityPrivacyCanary,
  runVisualResponsiveMatrix,
} from "./browser/privacy-presentation-scenarios.ts";
import {
  runAdminCredentials,
  runBackups,
  runCapabilityAudit,
  runInvocations,
  runOverview,
  runOverviewInvocationSystemCanary,
  runSystemAdministrationCanary,
  runSystemStatus,
} from "./browser/system-scenarios.ts";
import {
  runAuthFlows,
  runServerCatalogReads,
  runServerCreateUpdate,
  runServerCredentials,
  runServerDisconnectDelete,
  runServerManagementCanary,
  runServerOperations,
} from "./browser/server-scenarios.ts";
import {
  runAuthenticationEpoch,
  runFragmentStorage,
  runMutationState,
  runPriorSessionResponseIsolationCanary,
  runProtocol,
  runReadGeneration,
  runSessionLifecycleCanary,
  runShellPrimitives,
} from "./browser/lifecycle-scenarios.ts";
import {
  runDevelopmentControlPlane,
  runDevelopmentLiveReload,
} from "./browser/development-scenarios.ts";

interface BridgeInput {
  version: 1;
  scenario:
    | "shell-load"
    | "browser-protocol"
    | "session-lifecycle-canary"
    | "fragment-storage"
    | "authentication-epoch"
    | "read-generation"
    | "mutation-state"
    | "shell-primitives"
    | "accessibility-keyboard-responsive"
    | "visual-responsive-matrix"
    | "secret-storage-privacy"
    | "secret-sinks"
    | "prior-session-response-isolation-canary"
    | "overview-invocation-system-canary"
    | "server-management-canary"
    | "access-management-read-canary"
    | "system-administration-canary"
    | "visual-accessibility-privacy-canary"
    | "admin-credentials"
    | "backups"
    | "capability-audit"
    | "principals"
    | "principal-credentials"
    | "grant-reads-create"
    | "grant-correction"
    | "request-reads"
    | "request-adjudication"
    | "overview"
    | "invocations"
    | "system-status"
    | "server-catalog-reads"
    | "server-create-update"
    | "server-operations"
    | "server-credentials"
    | "server-disconnect-delete"
    | "auth-flows"
    | "development-live-reload"
    | "development-control-plane";
  base_url: string;
  admin_bearer: string;
  browser_kind?: "chromium" | "firefox" | "webkit";
  fixture_root?: string;
}

const inputLines = createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});

const inputIterator = inputLines[Symbol.asyncIterator]();

async function readBoundedInput(): Promise<unknown> {
  const next = await inputIterator.next();
  if (next.done || Buffer.byteLength(next.value, "utf8") > 16 * 1024)
    fail("invalid bridge input");
  try {
    return JSON.parse(next.value) as unknown;
  } catch {
    fail("invalid bridge JSON");
  }
}

function parseInitialInput(value: unknown): BridgeInput {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    (Object.keys(value).sort().join(",") !==
      "admin_bearer,base_url,scenario,version" &&
      Object.keys(value).sort().join(",") !==
        "admin_bearer,base_url,browser_kind,scenario,version" &&
      Object.keys(value).sort().join(",") !==
        "admin_bearer,base_url,fixture_root,scenario,version") ||
    !("version" in value) ||
    value.version !== 1 ||
    !("scenario" in value) ||
    (value.scenario !== "shell-load" &&
      value.scenario !== "browser-protocol" &&
      value.scenario !== "session-lifecycle-canary" &&
      value.scenario !== "fragment-storage" &&
      value.scenario !== "authentication-epoch" &&
      value.scenario !== "read-generation" &&
      value.scenario !== "mutation-state" &&
      value.scenario !== "shell-primitives" &&
      value.scenario !== "accessibility-keyboard-responsive" &&
      value.scenario !== "visual-responsive-matrix" &&
      value.scenario !== "secret-storage-privacy" &&
      value.scenario !== "secret-sinks" &&
      value.scenario !== "prior-session-response-isolation-canary" &&
      value.scenario !== "overview-invocation-system-canary" &&
      value.scenario !== "server-management-canary" &&
      value.scenario !== "access-management-read-canary" &&
      value.scenario !== "system-administration-canary" &&
      value.scenario !== "visual-accessibility-privacy-canary" &&
      value.scenario !== "admin-credentials" &&
      value.scenario !== "backups" &&
      value.scenario !== "capability-audit" &&
      value.scenario !== "principals" &&
      value.scenario !== "principal-credentials" &&
      value.scenario !== "grant-reads-create" &&
      value.scenario !== "grant-correction" &&
      value.scenario !== "request-reads" &&
      value.scenario !== "request-adjudication" &&
      value.scenario !== "overview" &&
      value.scenario !== "invocations" &&
      value.scenario !== "system-status" &&
      value.scenario !== "server-catalog-reads" &&
      value.scenario !== "server-create-update" &&
      value.scenario !== "server-operations" &&
      value.scenario !== "server-credentials" &&
      value.scenario !== "server-disconnect-delete" &&
      value.scenario !== "auth-flows" &&
      value.scenario !== "development-live-reload" &&
      value.scenario !== "development-control-plane") ||
    !("base_url" in value) ||
    typeof value.base_url !== "string" ||
    !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(value.base_url) ||
    !("admin_bearer" in value) ||
    typeof value.admin_bearer !== "string" ||
    value.admin_bearer.length === 0 ||
    ("browser_kind" in value &&
      value.browser_kind !== "chromium" &&
      value.browser_kind !== "firefox" &&
      value.browser_kind !== "webkit") ||
    (value.scenario === "development-live-reload") !==
      "fixture_root" in value ||
    ("fixture_root" in value &&
      (typeof value.fixture_root !== "string" ||
        !isAbsolute(value.fixture_root)))
  ) {
    fail("invalid bridge input");
  }
  return value as BridgeInput;
}

let browser: Browser | undefined;

try {
  let input = parseInitialInput(await readBoundedInput());
  const baseURL = input.base_url;
  const initialBearer = input.admin_bearer;
  input = { ...input, admin_bearer: "" };
  const browserType =
    input.browser_kind === "firefox"
      ? firefox
      : input.browser_kind === "webkit"
        ? webkit
        : chromium;
  browser = await browserType.launch({ headless: true });
  const context = await browser.newContext({
    baseURL,
    serviceWorkers: "block",
  });
  const externalRequests: string[] = [];
  const originFailures: string[] = [];
  const requestHeaderChecks: Array<Promise<void>> = [];
  let requests = 0;
  context.on("request", (request) => {
    requests += 1;
    if (!request.url().startsWith(baseURL))
      externalRequests.push(request.url());
    if (
      request.url().startsWith(`${baseURL}/api/`) &&
      request.method() !== "GET"
    ) {
      requestHeaderChecks.push(
        request.allHeaders().then((headers) => {
          if (headers.origin !== baseURL) originFailures.push(request.url());
        }),
      );
    }
  });
  const consoleFailures: string[] = [];
  context.on("page", (candidate) => {
    candidate.on("console", (message) => {
      if (
        message.type() === "error" &&
        !message
          .text()
          .startsWith(
            "Failed to load resource: the server responded with a status of 401",
          ) &&
        !(
          input.scenario === "overview" &&
          (message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            ) ||
            message
              .text()
              .startsWith(
                "Failed to load resource: the server responded with a status of 503",
              ))
        ) &&
        !(
          input.scenario === "invocations" &&
          (message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            ) ||
            message
              .text()
              .startsWith(
                "Failed to load resource: the server responded with a status of 404",
              ))
        ) &&
        !(
          input.scenario === "server-catalog-reads" &&
          message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            )
        ) &&
        !(
          input.scenario === "server-create-update" &&
          [409, 412, 428, 502].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-operations" &&
          [412, 502].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-disconnect-delete" &&
          message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 412",
            )
        ) &&
        !(
          input.scenario === "auth-flows" &&
          [409, 412].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-credentials" &&
          [412, 503].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        )
      ) {
        consoleFailures.push(message.text());
      }
    });
    candidate.on("pageerror", (error) => consoleFailures.push(error.name));
  });
  const page = await context.newPage();
  await loadShell(
    page,
    input.scenario !== "development-live-reload" &&
      input.scenario !== "development-control-plane",
  );

  if (input.scenario === "shell-load") {
    if (externalRequests.length !== 0) fail("external shell request");
    process.stdout.write('{"event":"shell_loaded"}\n');
    process.on("SIGTERM", () => {});
    setInterval(() => {}, 60 * 60 * 1000);
  } else {
    if (input.scenario === "development-control-plane") {
      await runDevelopmentControlPlane(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "development-live-reload") {
      await runDevelopmentLiveReload(
        browser.version(),
        page,
        initialBearer,
        input.fixture_root ?? fail("missing development fixture root"),
        () => requests,
      );
    } else if (input.scenario === "session-lifecycle-canary") {
      await runSessionLifecycleCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "fragment-storage") {
      await runFragmentStorage(browser.version(), page, () => requests);
    } else if (input.scenario === "authentication-epoch") {
      await runAuthenticationEpoch(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "read-generation") {
      await runReadGeneration(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "mutation-state") {
      await runMutationState(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "shell-primitives") {
      await runShellPrimitives(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "visual-accessibility-privacy-canary") {
      await runVisualAccessibilityPrivacyCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "secret-storage-privacy") {
      await runSecretStoragePrivacy(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "visual-responsive-matrix") {
      await runVisualResponsiveMatrix(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "accessibility-keyboard-responsive") {
      await runAccessibilityKeyboardResponsive(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "secret-sinks") {
      await runSecretSinks(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "prior-session-response-isolation-canary") {
      await runPriorSessionResponseIsolationCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "overview-invocation-system-canary") {
      await runOverviewInvocationSystemCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-management-canary") {
      await runServerManagementCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "access-management-read-canary") {
      await runAccessManagementReadCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "system-administration-canary") {
      await runSystemAdministrationCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "admin-credentials") {
      await runAdminCredentials(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "backups") {
      await runBackups(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "capability-audit") {
      await runCapabilityAudit(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "principals") {
      await runPrincipals(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "principal-credentials") {
      await runPrincipalCredentials(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "grant-reads-create") {
      await runGrantReadsCreate(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "grant-correction") {
      await runGrantCorrection(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "request-reads") {
      await runRequestReads(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "request-adjudication") {
      await runRequestAdjudication(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "overview") {
      await runOverview(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "invocations") {
      await runInvocations(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "system-status") {
      await runSystemStatus(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-disconnect-delete") {
      await runServerDisconnectDelete(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "auth-flows") {
      await runAuthFlows(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-credentials") {
      await runServerCredentials(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-operations") {
      await runServerOperations(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-create-update") {
      await runServerCreateUpdate(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-catalog-reads") {
      await runServerCatalogReads(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else {
      await runProtocol(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
        readBoundedInput,
      );
    }
    await Promise.all(requestHeaderChecks);
    const expectedOAuthOpen =
      "https://issuer.example/authorize?client_id=safe&state=one-time-state&code_challenge=pkce";
    const externalFailure =
      input.scenario === "auth-flows"
        ? externalRequests.length !== 1 ||
          externalRequests[0] !== expectedOAuthOpen
        : externalRequests.length !== 0;
    const expectedConsoleFailures =
      (input.scenario === "server-create-update" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 400"),
        )) ||
      (input.scenario === "principals" &&
        consoleFailures.length === 8 &&
        consoleFailures.filter((value) =>
          value.includes("server responded with a status of 409"),
        ).length === 5 &&
        consoleFailures.filter((value) =>
          value.includes("server responded with a status of 503"),
        ).length === 2 &&
        consoleFailures.filter((value) =>
          value.includes("server responded with a status of 412"),
        ).length === 1) ||
      (input.scenario === "principal-credentials" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 412"),
        )) ||
      (input.scenario === "grant-reads-create" &&
        consoleFailures.length >= 3 &&
        consoleFailures.length <= 4 &&
        [400, 409, 503].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        )) ||
      (input.scenario === "grant-correction" &&
        consoleFailures.length === 3 &&
        [400, 409, 503].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        )) ||
      (input.scenario === "request-reads" &&
        consoleFailures.length >= 1 &&
        consoleFailures.length <= 2 &&
        consoleFailures.every((value) =>
          value.includes("server responded with a status of 409"),
        )) ||
      (input.scenario === "backups" &&
        consoleFailures.length === 2 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 503"),
        )) ||
      (input.scenario === "admin-credentials" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 409"),
        )) ||
      (input.scenario === "request-adjudication" &&
        consoleFailures.length === 3 &&
        [400, 412, 503].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        ));
    if (
      externalFailure ||
      originFailures.length !== 0 ||
      (consoleFailures.length !== 0 && !expectedConsoleFailures)
    ) {
      fail(
        `unexpected browser protocol side effect (external=${externalRequests.length}, origin=${originFailures.length}, console=${consoleFailures.length}, console_classes=${consoleFailures.map((value) => /^Failed to load resource: the server responded with a status of ([0-9]{3})/.exec(value)?.[1] ?? "other").join(",")})`,
      );
    }
    if (
      (await page.evaluate(() => document.cookie)).includes(
        "mcp_gateway_session",
      )
    ) {
      fail("HttpOnly session cookie became script-visible");
    }
    await browser.close();
  }
} catch (error) {
  if (browser !== undefined) await browser.close();
  const detail =
    error instanceof Error
      ? error.message.replace(/mgw_admin_[A-Za-z0-9_-]+/g, "[redacted]")
      : "unknown";
  process.stderr.write(`browser coordinator failed: ${detail}\n`);
  process.exitCode = 3;
}
