package version

var (
	Version   = "1.4.0"
	Commit    = "dev"
	BuildTime = "unknown"
)

func String() string {
	if Commit == "" || Commit == "dev" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
