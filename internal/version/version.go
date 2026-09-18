// Package version holds the coxswain build version. The default is "dev"; a release build overrides it with
// -ldflags "-X github.com/nphattai/coxswain/internal/version.Version=<tag>".
package version

// Version is the build version, set at link time. It stays "dev" for a plain `go build`.
var Version = "dev"
