// Command sweep removes empty directories, counting directories that hold
// only .DS_Store files as empty. See internal/cli for the command itself.
package main

import (
	"context"
	"os"
	"runtime/debug"

	"charm.land/fang/v2"

	"github.com/synmux/sweep/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=…". When it is
// empty, the module version from the build info is used if there is one.
var version string

func main() {
	options := append(versionOptions(), fang.WithNotifySignal(os.Interrupt))

	err := fang.Execute(context.Background(), cli.NewCommand(cli.DefaultEnvironment()), options...)
	os.Exit(cli.ExitCode(err))
}

// versionOptions reports the version and VCS revision baked into the binary.
func versionOptions() []fang.Option {
	var options []fang.Option
	buildInfo, hasBuildInfo := debug.ReadBuildInfo()
	switch {
	case version != "":
		options = append(options, fang.WithVersion(version))
	case hasBuildInfo && buildInfo.Main.Version != "" && buildInfo.Main.Version != "(devel)":
		options = append(options, fang.WithVersion(buildInfo.Main.Version))
	}
	if !hasBuildInfo {
		return options
	}
	for _, setting := range buildInfo.Settings {
		if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
			options = append(options, fang.WithCommit(setting.Value[:7]))
		}
	}
	return options
}
