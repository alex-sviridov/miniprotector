// Mirrors policy-server's own attachDestination computation
// (src/cmd/policy-server/server.go) client-side: a storage policy's
// dialable address is its checked-in hostname paired with its own port.
// No endpoint returns this pre-joined for a storage policy, so it's
// computed here from data the app already fetches.
export function resolveStoreAddress(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    const checkin = (policy.checkins || []).find((c) => c.hostname === storeHost)
    if (checkin) return `${storeHost}:${policy.port}`
  }
  return null
}
