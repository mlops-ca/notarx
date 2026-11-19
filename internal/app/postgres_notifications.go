package app

import "context"

type postgresNotificationSource interface {
	Listen(context.Context, string) <-chan struct{}
}

func notificationWake(ctx context.Context, store any, channel string) <-chan struct{} {
	if source, ok := store.(postgresNotificationSource); ok {
		return source.Listen(ctx, channel)
	}
	return nil
}

// mergeWakes fans several wake channels into one. Nil channels (a store without notifications) are ignored; the
// result is nil when none remain, so a select on it never fires.
func mergeWakes(ctx context.Context, wakes []<-chan struct{}) <-chan struct{} {
	live := make([]<-chan struct{}, 0, len(wakes))
	for _, wake := range wakes {
		if wake != nil {
			live = append(live, wake)
		}
	}
	if len(live) == 0 {
		return nil
	}
	merged := make(chan struct{}, 1)
	for _, wake := range live {
		go func(wake <-chan struct{}) {
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-wake:
					if !ok {
						return
					}
					select {
					case merged <- struct{}{}:
					default:
					}
				}
			}
		}(wake)
	}
	return merged
}
