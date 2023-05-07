package powershell

import (
	"os"

	"github.com/bishopfox/sliver/client/console"
	"github.com/desertbit/grumble"
)

// PowerShellImportCmd - Import powershell script
func PowerShellImportCmd(ctx *grumble.Context, con *console.SliverConsoleClient) {
	scriptPath := ctx.Args.String("filepath")

	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		con.PrintErrorf("%s", err.Error())
		return
	}

	script = scriptBytes
	con.Printf("%s imported successfully\n", scriptPath)

	//con.App.RunCommand(strings.Split(fmt.Sprintf("%s -t %d %s loadmodule%s", sliverCommand, timeout, PSpath, sEnc), " "))

	//con.App.RunCommand(strings.Split(fmt.Sprintf("execute-assembly -t %s -i %s loadmodule%s", timeout, PSpath, sEnc), " "))

}
