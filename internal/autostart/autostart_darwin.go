//go:build darwin

package autostart

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const launchAgentLabel = "io.github.brbbruno.agent-bridge"

func launchAgentPath(options Options) string {
	return filepath.Join(options.HomeDir, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func command(executable string) string { return `"` + executable + `" daemon start` }

func Enable(options Options) (Status, error) {
	if options.Executable == "" || options.HomeDir == "" {
		return Status{}, fmt.Errorf("diretório pessoal ou executável ausente")
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(options.Executable)); err != nil {
		return Status{}, err
	}
	data := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>daemon</string><string>start</string></array>
<key>RunAtLoad</key><true/>
<key>AbandonProcessGroup</key><true/>
</dict></plist>
`, launchAgentLabel, escaped.String()))
	path := launchAgentPath(options)
	if err := writeAtomic(path, data); err != nil {
		return Status{}, err
	}
	return Get(options)
}

func Disable(options Options) error {
	err := os.Remove(launchAgentPath(options))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Get(options Options) (Status, error) {
	path := launchAgentPath(options)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{Location: path}, nil
	}
	if err != nil {
		return Status{}, err
	}
	arguments, label, runAtLoad, abandonProcessGroup, err := parseLaunchAgent(data)
	if err != nil {
		return Status{}, err
	}
	status := Status{Location: path}
	if label == launchAgentLabel && runAtLoad && abandonProcessGroup && len(arguments) == 3 && arguments[1] == "daemon" && arguments[2] == "start" {
		status.Enabled = true
		status.Command = command(arguments[0])
		status.MatchesExecutable = filepath.Clean(arguments[0]) == filepath.Clean(options.Executable)
	}
	return status, nil
}

func parseLaunchAgent(data []byte) ([]string, string, bool, bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var key string
	var label string
	var arguments []string
	var inArguments bool
	var runAtLoad bool
	var abandonProcessGroup bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", false, false, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			switch token.Name.Local {
			case "key":
				if err := decoder.DecodeElement(&key, &token); err != nil {
					return nil, "", false, false, err
				}
			case "array":
				inArguments = key == "ProgramArguments"
			case "string":
				var value string
				if err := decoder.DecodeElement(&value, &token); err != nil {
					return nil, "", false, false, err
				}
				if inArguments {
					arguments = append(arguments, value)
				} else if key == "Label" {
					label = value
				}
				key = ""
			case "true":
				switch key {
				case "RunAtLoad":
					runAtLoad = true
				case "AbandonProcessGroup":
					abandonProcessGroup = true
				}
				key = ""
			}
		case xml.EndElement:
			if token.Name.Local == "array" {
				inArguments = false
				key = ""
			}
		}
	}
	if len(arguments) == 0 || label == "" {
		return nil, "", false, false, fmt.Errorf("plist de início automático inválido")
	}
	return arguments, label, runAtLoad, abandonProcessGroup, nil
}
