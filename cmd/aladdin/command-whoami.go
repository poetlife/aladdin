package main

import (
	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
)

func newWhoAmICommand() *cobra.Command {
	return markAuthenticatedOnly(&cobra.Command{
		Use:   "whoami",
		Short: "打印当前凭证对应的主体",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := identityv1.NewIdentityServiceClient(c.Conn()).WhoAmI(ctx, &identityv1.WhoAmIRequest{})
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]string{
					"subject_id":    resp.GetSubjectId(),
					"subject_type":  resp.GetSubjectType(),
					"default_scope": resp.GetDefaultScope(),
				})
			}
			printf(cmd.OutOrStdout(), "%s (%s)  默认作用域=%q\n",
				resp.GetSubjectId(), resp.GetSubjectType(), resp.GetDefaultScope())
			return nil
		},
	})
}
