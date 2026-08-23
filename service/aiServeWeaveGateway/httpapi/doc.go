// Package httpapi is the Gateway's caller-facing front door: an
// OpenAI-compatible HTTP API that translates wire requests into
// common/runtime calls, dispatches them through a scheduler.Scheduler, and
// translates the result back — SSE, frame by frame, for a streaming chat
// request.
//
// Protocol conversion happens only here, at the system boundary, matching
// README.md's "外部协议只存在于系统边界": everything past New's returned
// handler talks in common/runtime types.
package httpapi
