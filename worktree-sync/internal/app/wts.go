package app

import "context"

type Request struct {
	Action  string
	Args    []string
	Options map[string]any
}

type Controller interface {
	Execute(context.Context, Request) (string, error)
}
