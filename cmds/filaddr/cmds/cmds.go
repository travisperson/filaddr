package cmds

import (
	"github.com/urfave/cli/v2"
)

var Commands = []*cli.Command{
	cmdCollect,
	cmdControl,
	cmdHttp,
}
