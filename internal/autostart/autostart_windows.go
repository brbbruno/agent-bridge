//go:build windows

package autostart

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	defaultRegistryKey = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName          = "agent-bridge"
)

func registryPath(options Options) string {
	if options.RegistryKey != "" {
		return options.RegistryKey
	}
	return defaultRegistryKey
}

func command(executable string) string { return `"` + executable + `" daemon start` }

func Enable(options Options) (Status, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, registryPath(options), registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return Status{}, err
	}
	defer key.Close()
	if err := key.SetStringValue(valueName, command(options.Executable)); err != nil {
		return Status{}, err
	}
	return Get(options)
}

func Disable(options Options) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath(options), registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return err
	}
	defer key.Close()
	err = key.DeleteValue(valueName)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	return err
}

func Get(options Options) (Status, error) {
	path := registryPath(options)
	location := `HKCU\` + path + `\` + valueName
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return Status{Location: location}, nil
		}
		return Status{}, err
	}
	defer key.Close()
	registered, _, err := key.GetStringValue(valueName)
	if err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return Status{Location: location}, nil
		}
		return Status{}, err
	}
	executable := registeredExecutable(registered)
	return Status{Enabled: true, Command: registered, Location: location, MatchesExecutable: strings.EqualFold(filepath.Clean(executable), filepath.Clean(options.Executable))}, nil
}

func registeredExecutable(registered string) string {
	registered = strings.TrimSpace(registered)
	if strings.HasPrefix(registered, `"`) {
		if end := strings.Index(registered[1:], `"`); end >= 0 {
			return registered[1 : end+1]
		}
		return ""
	}
	fields := strings.Fields(registered)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
