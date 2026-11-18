package version

// Version is the build stamped into source builds (go build / bootstrap.sh
// --binary). Release binaries override it via -ldflags, but anything built
// without that flag reports this string, and the panel's
// AgentBinaryVersion()/target_version derive from it — so it must be kept in
// step with the VERSION file (TestVersionMatchesVersionFile enforces that).
// Forgetting it in v1.6.0 (ec98a7c) shipped agents that misreported 1.5.x.
var (
	Version   = "1.6.3"
	Commit    = "dev"
	BuildTime = "unknown"
)

func String() string {
	if Commit == "" || Commit == "dev" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
