// Package server is the local web server behind the dashboard. It listens on
// 127.0.0.1 only and serves the embedded web UI, the JSON API, a live event
// stream and terminal WebSockets.
//
// Every request is authenticated with a per-launch session cookie and must
// carry a valid Host header (DNS-rebinding protection) and, for state-changing
// requests and WebSockets, a same-origin Origin header (CSRF protection).
package server
