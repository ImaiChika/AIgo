package storage

import (
	"context"
	"errors"
)

var ErrAuthoringDenied = errors.New("当前身份无所需命题权限，或身份已被撤销")

// AuthoringAccessStore serializes current-identity authorization with writes.
type AuthoringAccessStore interface {
	WithAuthoringAccess(context.Context, string, string, func(context.Context, []string) error) error
}
