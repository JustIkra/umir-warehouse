package bootstrap

import "github.com/JustIkra/umir-warehouse/internal/adapters/authkeys"

type AuthKeyOptions = authkeys.Options

func NewAuthSigningKeys(options AuthKeyOptions) (*authkeys.Client, error) {
	return authkeys.NewClient(options)
}
