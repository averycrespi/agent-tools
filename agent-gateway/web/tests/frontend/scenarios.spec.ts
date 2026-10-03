import { test, syntheticBearer } from "./fixture.ts";
import { registerCapture } from "./capture.ts";
import {
  runPrincipals,
  runPrincipalCredentials,
  runGrantReadsCreate,
  runGrantCorrection,
  runRequestReads,
  runRequestAdjudication,
} from "../browser/access-scenarios.ts";
import {
  runBackups,
  runAdminCredentials,
  runOverview,
  runInvocations,
  runSystemStatus,
} from "../browser/system-scenarios.ts";
import {
  runServerCreateUpdate,
  runServerOperations,
  runServerDisconnectDelete,
  runAuthFlows,
  runServerCredentials,
  runServerCatalogReads,
} from "../browser/server-scenarios.ts";

const scenarios = {
  principals: runPrincipals,
  "principal-credentials": runPrincipalCredentials,
  "grant-reads-create": runGrantReadsCreate,
  "grant-correction": runGrantCorrection,
  "request-reads": runRequestReads,
  "request-adjudication": runRequestAdjudication,
  backups: runBackups,
  "admin-credentials": runAdminCredentials,
  overview: runOverview,
  invocations: runInvocations,
  "system-status": runSystemStatus,
  "auth-flows": runAuthFlows,
  "server-catalog-reads": runServerCatalogReads,
};
for (const [name, scenario] of Object.entries({
  "server-create-update": runServerCreateUpdate,
  "server-operations": runServerOperations,
  "server-disconnect-delete": runServerDisconnectDelete,
  "server-credentials": runServerCredentials,
})) {
  test(name, async ({ browser, page, frontend }) => {
    registerCapture(page, name);
    await scenario(
      browser.version(),
      page,
      frontend.origin,
      syntheticBearer,
      frontend.requests,
    );
  });
}
for (const [name, scenario] of Object.entries(scenarios)) {
  test(name, async ({ browser, context, page, frontend }) => {
    registerCapture(page, name);
    await scenario(
      browser.version(),
      context,
      page,
      frontend.origin,
      syntheticBearer,
      frontend.requests,
    );
  });
}
