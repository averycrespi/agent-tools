import { randomUUID } from "node:crypto";

export default function setup() {
  // Global setup runs once before workers inherit the environment. Always
  // replace caller-supplied IDs so old artifacts cannot qualify a fresh run.
  process.env.FRONTEND_BROWSER_RUN = randomUUID();
}
