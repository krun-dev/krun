package control

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/krun-dev/krun/internal/localize"
)

func retain(ctx context.Context, store *Store, interval time.Duration) {
	store.RunRetention(ctx, interval, func(err error) {
		if ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, localize.Text(localize.Language(ctx), "krun retention cleanup failed:"), localize.Error(localize.Language(ctx), err))
		}
	})
}
