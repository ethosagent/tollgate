package main

import "runtime/debug"

// devVersion is what a build without a version stamp reports.
const devVersion = "0.0.0-dev"

// version is stamped by the release build: -ldflags "-X main.version=v1.2.3".
var version = devVersion

// buildVersion is the version tollgated reports: the release stamp if there
// is one, else the module version `go install ...@vX.Y.Z` records.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	return versionFrom(version, info, ok)
}

// versionFrom picks the reported version from the ldflags stamp and the
// binary's build info. A checkout build's module version is "(devel)".
func versionFrom(stamp string, info *debug.BuildInfo, ok bool) string {
	if stamp != devVersion || !ok || info == nil {
		return stamp
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return stamp
}
