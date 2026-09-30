//go:build !windows

package e2e

import (
	"errors"
	"time"
)

type ptyRunner struct{}

func startDevinPTY(string, []string, []string, string) (*ptyRunner, error) {
	return nil, errors.New("ConPTY só está disponível no Windows")
}
func (*ptyRunner) Send(string) error                  { return errors.New("ConPTY só está disponível no Windows") }
func (*ptyRunner) WaitFor(string, time.Duration) bool { return false }
func (*ptyRunner) Output() string                     { return "" }
func (*ptyRunner) Close()                             {}
