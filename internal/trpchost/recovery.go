package trpchost

import (
	"context"
	"log/slog"

	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

func init() { filter.Register("monitor_recovery", recovery, nil) }
func recovery(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (rsp interface{}, err error) {
	defer func() {
		if recover() != nil {
			slog.Error("http_handler_panicked")
			rsp = nil
			err = errs.New(errs.RetServerSystemErr, "internal error")
		}
	}()
	return next(ctx, req)
}
