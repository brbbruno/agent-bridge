//go:build windows

package e2e

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestConPTYExecProducesOutput(t *testing.T) {
	windowsRoot := os.Getenv("WINDIR")
	if windowsRoot == "" {
		windowsRoot = `C:\Windows`
	}
	commandPrompt := filepath.Join(windowsRoot, "System32", "cmd.exe")
	runner, err := startDevinPTY(commandPrompt, []string{"/c", "echo", "CONPTY_READY"}, os.Environ(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if !runner.WaitFor("CONPTY_READY", 10*time.Second) {
		t.Fatalf("ConPTY não capturou saída: %q", runner.Output())
	}
}

type ptyRunner struct {
	process  windows.Handle
	console  windows.Handle
	input    *os.File
	output   *os.File
	buffer   bytes.Buffer
	mu       sync.Mutex
	done     chan struct{}
	readDone chan struct{}
}

func startDevinPTY(executable string, args, environment []string, directory string) (*ptyRunner, error) {
	inputRead, inputWrite, err := makePipe()
	if err != nil {
		return nil, err
	}
	outputRead, outputWrite, err := makePipe()
	if err != nil {
		windows.CloseHandle(inputRead)
		windows.CloseHandle(inputWrite)
		return nil, err
	}
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 120, Y: 40}, inputRead, outputWrite, 0, &console); err != nil {
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, err
	}
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	if err := updatePtyAttribute(attributes, console); err != nil {
		attributes.Delete()
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	defer attributes.Delete()

	commandArgs := append([]string{executable}, args...)
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(commandArgs))
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	currentDirectory, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	environmentBlock := makeEnvironmentBlock(environment)
	startup := &windows.StartupInfoEx{
		StartupInfo:             windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES},
		ProcThreadAttributeList: attributes.List(),
	}
	processInfo := new(windows.ProcessInformation)
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NEW_PROCESS_GROUP)
	if err := windows.CreateProcess(application, commandLine, nil, nil, false, flags, &environmentBlock[0], currentDirectory, &startup.StartupInfo, processInfo); err != nil {
		windows.ClosePseudoConsole(console)
		closeHandles(inputWrite, outputRead)
		return nil, err
	}
	_ = windows.CloseHandle(processInfo.Thread)
	runner := &ptyRunner{
		process:  processInfo.Process,
		console:  console,
		input:    os.NewFile(uintptr(inputWrite), "devin-conpty-input"),
		output:   os.NewFile(uintptr(outputRead), "devin-conpty-output"),
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	go runner.readOutput()
	go func() {
		_, _ = windows.WaitForSingleObject(runner.process, windows.INFINITE)
		close(runner.done)
	}()
	return runner, nil
}

func makePipe() (windows.Handle, windows.Handle, error) {
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, attributes, 0); err != nil {
		return 0, 0, err
	}
	return readHandle, writeHandle, nil
}

func closeHandles(handles ...windows.Handle) {
	for _, handle := range handles {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}
}

func makeEnvironmentBlock(environment []string) []uint16 {
	values := append([]string(nil), environment...)
	sort.Slice(values, func(i, j int) bool { return strings.ToUpper(values[i]) < strings.ToUpper(values[j]) })
	block := utf16.Encode([]rune(strings.Join(values, "\x00") + "\x00\x00"))
	return block
}

func (p *ptyRunner) readOutput() {
	defer close(p.readDone)
	defer p.output.Close()
	buffer := make([]byte, 4096)
	for {
		count, err := p.output.Read(buffer)
		if count > 0 {
			p.mu.Lock()
			_, _ = p.buffer.Write(buffer[:count])
			p.mu.Unlock()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.mu.Lock()
				_, _ = p.buffer.WriteString("\n[PTY read error: " + err.Error() + "]\n")
				p.mu.Unlock()
			}
			return
		}
	}
}

func (p *ptyRunner) Send(text string) error {
	_, err := p.input.Write([]byte(text))
	return err
}

func (p *ptyRunner) Enter() error {
	_, err := p.input.Write([]byte{'\r'})
	return err
}

func (p *ptyRunner) SendLine(text string) error {
	if err := p.Send(text); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	return p.Enter()
}

func (p *ptyRunner) WaitFor(text string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		found := strings.Contains(p.buffer.String(), text)
		p.mu.Unlock()
		if found {
			return true
		}
		select {
		case <-p.done:
			select {
			case <-p.readDone:
			case <-time.After(500 * time.Millisecond):
			}
			p.mu.Lock()
			found = strings.Contains(p.buffer.String(), text)
			p.mu.Unlock()
			return found
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

func (p *ptyRunner) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffer.String()
}

func (p *ptyRunner) Close() {
	if p.input != nil {
		_, _ = p.input.Write([]byte{3})
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = windows.TerminateProcess(p.process, 1)
		_, _ = windows.WaitForSingleObject(p.process, uint32(5000))
	}
	if p.input != nil {
		_ = p.input.Close()
	}
	windows.ClosePseudoConsole(p.console)
	_ = windows.CloseHandle(p.process)
}
