package websocket

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkPerformanceOperationContext(b *testing.B) {
	for _, broadcast := range []bool{false, true} {
		b.Run(fmt.Sprint(broadcast), func(b *testing.B) {
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			c := &Connection{ctx: lifetime, writeTimeout: 30 * time.Second}
			parent := context.Background()
			if broadcast {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, time.Hour)
				defer cancel()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if broadcast {
					c.beginBroadcast(parent)
					c.endBroadcast()
				} else {
					_, cancel := c.operationContext(c.writeTimeout)
					cancel()
				}
			}
		})
	}
}

func BenchmarkPerformanceBroadcastDispatch(b *testing.B) {
	for _, count := range []int{1, 32, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			executor := &connectionExecutor{}
			defer executor.close()
			conns := make([]*Connection, count)
			fn := func(*Connection) error { return nil }
			_ = executor.run(conns, fn)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = executor.run(conns, fn)
			}
		})
	}
}
