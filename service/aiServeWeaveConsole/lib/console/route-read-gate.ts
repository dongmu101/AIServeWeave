/** createRouteReadGate rejects superseded reads and observations predating a write.
 * createRouteReadGate 拒绝被后续读取取代的结果，以及写入前的观测。 */
export function createRouteReadGate() {
  const versions = { current: 0, history: 0, status: 0 };
  return {
    begin(surface: keyof typeof versions) {
      const version = ++versions[surface];
      return () => versions[surface] === version;
    },
    invalidate() {
      versions.current++; versions.history++; versions.status++;
    },
  };
}
