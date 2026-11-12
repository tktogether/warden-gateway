package version

var (
	Version   = "1.3.8"
	Commit    = "dev"
	BuildTime = "unknown"
)

func String() string {
	if Commit == "" || Commit == "dev" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
