export const recoveryStates = [
  "healthy",
  "degraded",
  "recovering",
  "operator_action_required",
  "recovered",
];
export function trafficRecoveryFixture(health: string) {
  return {
    health,
    last_acknowledged:
      health === "recovered" ? "2026-10-07T09:00:00Z" : "2026-10-07T08:00:00Z",
    incident:
      health === "healthy"
        ? null
        : {
            first_failure: "2026-10-07T08:30:00Z",
            cause:
              health === "operator_action_required" ? "integrity" : "locked",
            stage: "begin",
            settlement: "not_started",
            recovery: health,
            recovery_cause:
              health === "operator_action_required" ? "integrity" : "locked",
            recovery_stage: health === "recovering" ? "validation" : "begin",
            sqlite_code: health === "operator_action_required" ? 11 : 5,
            affected: 3,
            discarded: 3,
          },
  };
}
