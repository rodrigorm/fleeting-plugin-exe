package main

import (
	exe "github.com/rodrigorm/fleeting-plugin-exe"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

func main() {
	plugin.Main(&exe.InstanceGroup{}, exe.Version)
}
