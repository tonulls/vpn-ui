package service

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/mymmrac/telego/telegoapi"
)

const telegramXrayReadinessTimeout = 5 * time.Second

func telegramCommandErrorRetryable(err error) bool {
	var apiErr *telegoapi.Error
	return err != nil && !errors.As(err, &apiErr)
}

// waitForTelegramXraySocks waits at most five seconds for the loopback SOCKS5
// listener, including a SOCKS greeting so a merely-open TCP port is not enough.
func waitForTelegramXraySocks() error {
	return waitForSocks5Listener(net.JoinHostPort("127.0.0.1", strconv.Itoa(telegramBotXrayPort)), telegramXrayReadinessTimeout)
}

func waitForSocks5Listener(address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		dialTimeout := min(remaining, 200*time.Millisecond)
		conn, err := net.DialTimeout("tcp4", address, dialTimeout)
		if err == nil {
			_ = conn.SetDeadline(deadline)
			_, err = conn.Write([]byte{0x05, 0x01, 0x00})
			if err == nil {
				var response [2]byte
				_, err = io.ReadFull(conn, response[:])
				if err == nil && response != [2]byte{0x05, 0x00} {
					err = fmt.Errorf("unexpected SOCKS5 greeting response %v", response)
				}
			}
			_ = conn.Close()
			if err == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		if err != nil {
			lastErr = err
		}
		if remaining = time.Until(deadline); remaining > 0 {
			time.Sleep(min(100*time.Millisecond, remaining))
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("listener did not complete SOCKS5 handshake")
	}
	return fmt.Errorf("Xray SOCKS listener was not ready within %s: %w", timeout, lastErr)
}
