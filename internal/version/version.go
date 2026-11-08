package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return "mio " + Version + " (commit " + Commit + ", built " + Date + ")"
}
