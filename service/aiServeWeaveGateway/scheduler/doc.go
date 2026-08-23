// Package scheduler picks which connected node serves a request. It reads
// the node table tunnelserver.Server already maintains — model inventory,
// capabilities, idle slot counts — and does not itself track any state: a
// Scheduler is a stateless view over the Server it was built with.
//
// Selection and retry policy live here rather than in httpapi so a future
// caller other than the HTTP front door (a gRPC front door, an internal job
// runner) gets the same behavior for free.
package scheduler
