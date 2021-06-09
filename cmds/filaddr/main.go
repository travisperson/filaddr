package main

import (
	"fmt"
	"os"

	"github.com/travisperson/filaddr/build"
	"github.com/travisperson/filaddr/cmds/filaddr/cmds"
	"github.com/travisperson/filaddr/internal/logging"

	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:     "filaddr",
		Usage:    "filaddr software suite",
		Version:  build.Version(),
		Commands: cmds.Commands,
	}

	err := app.Run(os.Args)
	if err != nil {
		logging.Logger.Errorw("exit", "error", err)
		os.Exit(1)
	}
}

var hello = &cli.Command{
	Name:  "hello",
	Usage: "say hello",
	Flags: []cli.Flag{},
	Action: func(cctx *cli.Context) error {
		fmt.Println("Hello")

		return nil
	},
}
