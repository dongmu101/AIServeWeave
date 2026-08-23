// Package e2e runs the real Agent against the real Gateway over a real
// network. Nothing here is a stand-in for a tunnel: the Agent's own
// tunnel.Manager dials three gRPC listeners over mutually authenticated TLS,
// with certificates issued by a CA the test creates on disk, and the Gateway's
// tunnelserver terminates those connections exactly as it would in production.
//
// It lives in its own package because it is the one place that depends on both
// services at once. Neither service imports the other, and keeping the test
// that joins them outside both is what preserves that.
//
// What is *not* real here is the inference backend: it is a scripted runtime
// implementation, because a test that needed a GPU would not run. The backend
// adapters have their own tests against their own protocols; this package is
// about what happens between the Agent and the Gateway.
package e2e
