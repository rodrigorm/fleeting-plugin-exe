package exe

import "gitlab.com/gitlab-org/fleeting/fleeting/plugin"

var (
	Name      = "fleeting-plugin-exe"
	VersionID = "dev"
	Revision  = "HEAD"
	Reference = "HEAD"
	BuiltAt   = "unknown"

	Version plugin.VersionInfo
)

func init() {
	Version = plugin.VersionInfo{
		Name:      Name,
		Version:   VersionID,
		Revision:  Revision,
		Reference: Reference,
		BuiltAt:   BuiltAt,
	}
}
