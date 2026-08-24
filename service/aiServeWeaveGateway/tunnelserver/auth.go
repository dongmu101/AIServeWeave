package tunnelserver

import (
	"context"

	"AIServeWeave/common/nodeid"
)

// peerNodeID returns the node identity asserted by the client certificate on
// ctx's connection. It is a thin alias for nodeid.FromPeer, kept so this
// package's callers do not have to name the shared package for a check this
// local to the tunnel stream.
func peerNodeID(ctx context.Context) (string, error) {
	return nodeid.FromPeer(ctx)
}
