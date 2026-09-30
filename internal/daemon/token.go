package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
)

func LoadOrCreateToken(home string) (string, error) {
	if err := config.EnsureHome(home); err != nil {
		return "", err
	}
	path := config.TokenPath(home)
	data, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", errors.New("daemon.token está vazio")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			for attempt := 0; attempt < 50; attempt++ {
				data, readErr := os.ReadFile(path)
				if readErr == nil {
					token := strings.TrimSpace(string(data))
					if token != "" {
						_ = os.Chmod(path, 0o600)
						return token, nil
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			return "", errors.New("daemon.token está vazio")
		}
		return "", err
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Clean(path), 0o600)
	return token, nil
}
