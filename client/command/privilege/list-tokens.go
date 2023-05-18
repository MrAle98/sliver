package privilege

/*
	Sliver Implant Framework
	Copyright (C) 2021  Bishop Fox

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bishopfox/sliver/client/command/settings"
	"github.com/bishopfox/sliver/client/console"
	"github.com/bishopfox/sliver/protobuf/clientpb"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
	"github.com/desertbit/grumble"
	"github.com/jedib0t/go-pretty/v6/table"
	"google.golang.org/protobuf/proto"
)

var ImpLevelMap = map[int32]string{
	0: "SecurityAnonymous",
	1: "SecurityIdentification",
	2: "SecurityImpersonation",
	3: "SecurityDelegation",
}

var TokenTypeMap = map[int32]string{
	1: "TokenPrimary",
	2: "TokenImpersonation",
}

// ImpersonateCmd - Windows only, impersonate a user token
func ListTokensCmd(ctx *grumble.Context, con *console.SliverConsoleClient) {
	session, beacon := con.ActiveTarget.GetInteractive()
	if session == nil && beacon == nil {
		return
	}

	tokens, err := con.Rpc.ListTokens(context.Background(), &sliverpb.ListTokensReq{
		Request: con.ActiveTarget.Request(ctx),
	})
	if err != nil {
		con.PrintErrorf("%s", err)
		return
	}

	if tokens.Response != nil && tokens.Response.Async {
		con.AddBeaconCallback(tokens.Response.TaskID, func(task *clientpb.BeaconTask) {
			err = proto.Unmarshal(task.Response, tokens)
			if err != nil {
				con.PrintErrorf("Failed to decode response %s\n", err)
				return
			}
			PrintTokens(tokens, ctx, con)
		})
		con.PrintAsyncResponse(tokens.Response)
	} else {
		PrintTokens(tokens, ctx, con)
	}
}

func filterTokens(tokens []*sliverpb.Token) []*sliverpb.Token {
	var filtered []*sliverpb.Token

	for _, t := range tokens {

		if strings.Contains(t.Username, "DWM-") || strings.Contains(t.Username, "LOCAL SERVICE") {

		}
	}
	return filtered
}

// Sort SortTokensByPrivilegeCount - Sorts a list of tokens based on the number of privileges associated to a token
func SortTokensByPrivilegeCount(tokens []*sliverpb.Token) []*sliverpb.Token {
	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].PrivilegesCount < tokens[j].PrivilegesCount
	})
	return tokens
}

// PrintTokens - Prints available tokens
func PrintTokens(listTokens *sliverpb.ListTokens, ctx *grumble.Context, con *console.SliverConsoleClient) {

	tw := table.NewWriter()
	tw.SetStyle(settings.GetTableStyle(con))

	tw.AppendHeader(table.Row{"tokenID", "LUID", "LogonType", "username", "privilegesCount", "TokenType", "impersonationLevel", "integrityLevel"})

	for _, token := range listTokens.Tokens {
		row := tokenRow(tw, token, con)
		tw.AppendRow(row)
	}

	settings.PaginateTable(tw, 0, true, false, con)
}

// procRow - Stylizes the token information
func tokenRow(tw table.Writer, token *sliverpb.Token, con *console.SliverConsoleClient) table.Row {
	color := console.Normal

	var row table.Row
	var integrityLevel string
	TokenType := TokenTypeMap[token.TokenType]
	var TokenImpLevel string

	if token.TokenType == 1 {
		if token.TokenIntegrity >= 0x1000 && token.TokenIntegrity < 0x2000 {
			integrityLevel = "LOW"
		} else if token.TokenIntegrity >= 0x2000 && token.TokenIntegrity < 0x3000 {
			integrityLevel = "MEDIUM"
		} else if token.TokenIntegrity >= 0x3000 && token.TokenIntegrity < 0x4000 {
			integrityLevel = "HIGH"
		} else if token.TokenIntegrity >= 0x4000 && token.TokenIntegrity < 0x5000 {
			integrityLevel = "SYSTEM"
		}
		TokenImpLevel = ""
	} else {
		integrityLevel = ""
		TokenImpLevel = TokenTypeMap[token.TokenImpLevel]
	}

	row = table.Row{
		fmt.Sprintf(color+"%x"+console.Normal, token.TokenId),
		fmt.Sprintf(color+"%x"+console.Normal, token.LogonSessionId),
		fmt.Sprintf(color+"%d"+console.Normal, token.LogonType),
		fmt.Sprintf(color+"%s"+console.Normal, token.Username),
		fmt.Sprintf(color+"%d"+console.Normal, token.PrivilegesCount),
		fmt.Sprintf(color+"%s"+console.Normal, TokenType),
		fmt.Sprintf(color+"%s"+console.Normal, TokenImpLevel),
		fmt.Sprintf(color+"%s"+console.Normal, integrityLevel),
	}

	return row
}
