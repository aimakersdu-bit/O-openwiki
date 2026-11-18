package version

import "fmt"

var (
	Version   = "unknown"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
}

func Get() Info {
	return Info{Version: Version, GitCommit: GitCommit, BuildTime: BuildTime}
}

func (i Info) String() string {
	return fmt.Sprintf("openwiki-orchestrator %s (commit: %s, built: %s)", i.Version, i.GitCommit, i.BuildTime)
}
