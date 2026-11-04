// Package claude implements discovery and resumption of Claude Code sessions.
//
// Discovery relies only on verified interfaces of the installed CLI:
// `claude agents --json` and the on-disk session stores under ~/.claude.
// Resumption uses `claude -p --resume <session-id> "<message>"`.
package claude
