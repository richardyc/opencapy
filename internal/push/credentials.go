//go:build !release

package push

// Embedded APNs credentials used when config.json has no APNs section.
// For release builds, create credentials_release.go (gitignored) with real values.
// See credentials_release.go.template for the format.
const embeddedKeyID  = ""
const embeddedTeamID = ""
const embeddedKeyP8  = ""
