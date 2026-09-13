package api

// This is a durable identity, not an HTTP route. Retained pre-cutover records
// must still fence equivalent v2 requests, including uncertain outcomes.
const serverCreateIdempotencyRoute = "/api/v1/servers"
