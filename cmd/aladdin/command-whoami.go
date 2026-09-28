package main

import (
	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/pkg/client"
)

func newWhoAmICommand() *cobra.Command {
	return markAuthenticatedOnly(&cobra.Command{
		Use:   "whoami",
		Short: "打印当前凭证对应的主体",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := client.NewService(c, identityv1connect.NewIdentityServiceClient).
				WhoAmI(ctx, connect.NewRequest(&identityv1.WhoAmIRequest{}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]string{
					"subject_id":    resp.Msg.GetSubjectId(),
					"subject_type":  resp.Msg.GetSubjectType(),
					"default_scope": resp.Msg.GetDefaultScope(),
				})
			}
			printf(cmd.OutOrStdout(), "%s (%s)  默认作用域=%q\n",
				resp.Msg.GetSubjectId(), resp.Msg.GetSubjectType(), resp.Msg.GetDefaultScope())
			return nil
		},
	})
}
