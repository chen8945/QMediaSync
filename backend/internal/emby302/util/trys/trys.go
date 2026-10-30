package trys

import "time"

// Try 尝试执行函数 fn
// 最多尝试 tryNum 次
// 每两次尝试的间隔时间为 interval
func Try(fn func() (err error), tryNum int, interval time.Duration) error {
	if tryNum <= 0 {
		return nil
	}

	var err error
	for range tryNum {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(interval)
	}

	return err
}
