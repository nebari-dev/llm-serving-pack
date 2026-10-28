// Package buildinfo names the running build, for the console's version footer.
package buildinfo

import "runtime/debug"

// Version is set with -ldflags "-X .../buildinfo.Version=v1.2.3" for a
// release. Otherwise it's the commit the binary was built from ("+dirty" with
// local changes), or "dev" when the build has no VCS stamp (go run).
var Version = ""

func Get() string {
	if Version != "" {
		return Version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}
