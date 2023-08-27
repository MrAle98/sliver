package helloworld

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/spf13/cobra"

	"github.com/bishopfox/sliver/client/console"
	"github.com/bishopfox/sliver/protobuf/clientpb"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
)

func HelloWorldCmd(cmd *cobra.Command, con *console.SliverConsoleClient, args []string) (err error) {
	session, beacon := con.ActiveTarget.GetInteractive()
	if session == nil && beacon == nil {
		return
	}

	if len(args) != 1 {
		con.PrintErrorf("Please specify an argument for param1.\n")
		return
	}

	param1 := args[0]
	param2, _ := cmd.Flags().GetUint32("intflag")
	param3, _ := cmd.Flags().GetBool("boolflag")

	out, err := con.Rpc.HelloWorld(context.Background(), &sliverpb.HelloWorldReq{
		Request: con.ActiveTarget.Request(cmd),
		Param1:  param1,
		Param2:  param2,
		Param3:  param3,
	})
	if err != nil {
		con.PrintErrorf("%s\n", err)
		return
	}

	if out.Response != nil && out.Response.Async {
		con.AddBeaconCallback(out.Response.TaskID, func(task *clientpb.BeaconTask) {
			err = proto.Unmarshal(task.Response, out)
			if err != nil {
				con.PrintErrorf("Failed to decode response %s\n", err)
				return
			}
			PrintHelloWorld(out, con)
		})
		con.PrintAsyncResponse(out.Response)
	} else {
		PrintHelloWorld(out, con)
	}

	return
}

func PrintHelloWorld(hw *sliverpb.HelloWorld, con *console.SliverConsoleClient) {
	if hw.Response != nil && hw.Response.Err != "" {
		con.PrintErrorf("%s\n", hw.Response.Err)
		return
	}

	con.PrintInfof("Here the output coming from the implant: %s\n", hw.Output)
}
