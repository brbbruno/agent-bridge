package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brbbruno/agent-bridge/internal/config"
)

const fakeChatID int64 = 424242

func TestDevinTwoWayE2E(t *testing.T) {
	focusedRework := os.Getenv("AGENT_BRIDGE_E2E_REWORK_ONLY") == "1"
	if os.Getenv("AGENT_BRIDGE_E2E") != "1" {
		t.Skip("defina AGENT_BRIDGE_E2E=1 para executar o e2e real com Devin")
	}
	if runtime.GOOS != "windows" {
		t.Skip("o e2e com Devin está configurado para Windows")
	}
	devinExe := os.Getenv("DEVIN_EXE")
	if devinExe == "" {
		devinExe = `C:\Users\bruno\AppData\Local\Programs\Devin\resources\app\extensions\windsurf\devin\bin\devin.exe`
	}
	if _, err := os.Stat(devinExe); err != nil {
		t.Fatalf("Devin CLI não encontrado: %v", err)
	}
	root := repoRoot(t)
	artifactRoot := os.Getenv("AGENT_BRIDGE_E2E_ARTIFACTS")
	if artifactRoot == "" {
		artifactRoot = filepath.Join(root, "artifacts", "e2e-"+time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(artifactRoot, "home")
	project := filepath.Join(artifactRoot, "repo")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := initializeGit(project); err != nil {
		t.Fatal(err)
	}
	fake := newFakeBot()
	apiServer := httptest.NewServer(fake)
	t.Cleanup(apiServer.Close)
	bridgeExe := ""
	var bridgeEnv []string
	var interactive *ptyRunner

	port := freePort(t)
	cfg := config.Default()
	cfg.Port = port
	cfg.Telegram = config.Telegram{BotToken: "e2e_fake_token_123", ChatID: fakeChatID, APIBase: apiServer.URL}
	cfg.StopWaitText = "2s"
	cfg.PermissionWaitText = "15s"
	cfg.QuestionWaitText = "15s"
	if err := config.Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.StatePath(home), []byte(`{"away":true,"queued_late_replies":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bridgeExe = filepath.Join(artifactRoot, "agent-bridge with space.exe")
	buildOutput, err := buildBridge(root, bridgeExe)
	writeArtifact(t, artifactRoot, "build.txt", []byte(buildOutput))
	if err != nil {
		t.Fatalf("build: %v\n%s", err, buildOutput)
	}
	originalDevin := []byte(`{"PreToolUse":[{"matcher":"^never_match_this$","hooks":[{"type":"command","command":"echo keep-devin"}]}]}` + "\n")
	originalClaude := []byte(`{"custom_test_metadata":{"keep":true},"hooks":{"PostToolUse":[{"matcher":"^NeverTool$","hooks":[{"type":"command","command":"echo keep-claude"}]}]}}` + "\n")
	devinHooksPath := filepath.Join(project, ".devin", "hooks.v1.json")
	claudeSettingsPath := filepath.Join(project, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(devinHooksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(claudeSettingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devinHooksPath, originalDevin, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettingsPath, originalClaude, 0o600); err != nil {
		t.Fatal(err)
	}
	writeArtifact(t, artifactRoot, "project-hooks-before.json", originalDevin)
	writeArtifact(t, artifactRoot, "claude-settings-before.json", originalClaude)

	bridgeEnv = withHome(os.Environ(), home)
	t.Cleanup(func() {
		if interactive != nil {
			interactive.Close()
		}
		if bridgeExe != "" {
			if err := runBridgeStatusStop(artifactRoot, bridgeEnv, bridgeExe); err != nil {
				t.Errorf("limpeza do daemon E2E: %v", err)
			}
		}
		messages, edits := fake.Snapshot()
		if data, err := json.MarshalIndent(messages, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(artifactRoot, "fake-telegram-messages.json"), data, 0o600)
		}
		if data, err := json.MarshalIndent(edits, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(artifactRoot, "fake-telegram-edits.json"), data, 0o600)
		}
		if data, err := json.MarshalIndent(fake.UpdatesSnapshot(), "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(artifactRoot, "fake-telegram-updates.json"), data, 0o600)
		}
		if data, err := os.ReadFile(config.LogPath(home)); err == nil {
			_ = os.WriteFile(filepath.Join(artifactRoot, "agent-bridge.log"), data, 0o600)
		}
	})
	if output, err := runCommand(artifactRoot, bridgeEnv, bridgeExe, "install", "--agent", "devin", "--scope", "project", "--project-dir", project); err != nil {
		t.Fatalf("install Devin: %v\n%s", err, output)
	}
	if output, err := runCommand(artifactRoot, bridgeEnv, bridgeExe, "install", "--agent", "claude", "--scope", "project", "--project-dir", project); err != nil {
		t.Fatalf("install Claude: %v\n%s", err, output)
	}

	if _, err := os.Stat(config.TokenPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon.token já existia antes do primeiro hook: %v", err)
	}

	var stopMu sync.Mutex
	stopReplies := map[string]int{}
	fake.SetResponder(func(message fakeMessage) {
		if !strings.HasPrefix(message.Text, "[devin ·") || len(message.Keyboard) > 0 || message.ForceReply {
			return
		}
		header := strings.SplitN(message.Text, "\n", 2)[0]
		stopMu.Lock()
		stopReplies[header]++
		first := stopReplies[header] == 1
		stopMu.Unlock()
		if first {
			fake.PushMessage(fakeChatID, message.ID, "Agora responda também com a palavra BANANA.")
		}
	})
	resultA := startDevin(t, devinExe, project, home, "Responda com uma frase curta e encerre o turno.")
	stopMessage, ok := fake.WaitSend(time.Now().Add(90*time.Second), func(message fakeMessage) bool {
		return strings.HasPrefix(message.Text, "[devin ·") && len(message.Keyboard) == 0
	})
	if !ok {
		t.Fatalf("E2E A: nenhum Stop enviado ao Telegram; artefatos: %s", artifactRoot)
	}
	_ = stopMessage
	outputA, err := waitSession(resultA, 120*time.Second)
	writeArtifact(t, artifactRoot, "scenario-a-stop-banana.txt", []byte(outputA))
	if err != nil || !strings.Contains(strings.ToUpper(outputA), "BANANA") {
		t.Fatalf("E2E A: esperava BANANA; err=%v output=%s", err, outputA)
	}
	assertFakeEditPreserves(t, fake, stopMessage.ID, stopMessage.Text, "Resposta recebida")
	if _, err := os.Stat(config.TokenPath(home)); err != nil {
		t.Fatalf("primeiro hook não auto-iniciou o daemon: %v", err)
	}
	status, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "daemon", "status")
	if err != nil || !strings.Contains(status, "Daemon ativo há") {
		t.Fatalf("daemon não ficou ativo após o primeiro hook: err=%v status=%s", err, status)
	}

	fake.SetResponder(stopOnceResponder(fake, "Turno concluído."))
	if !focusedRework {
		resultApprove := startDevin(t, devinExe, project, home, "Use exec to run exactly: python -c \"open('logs/permission-approved.txt','w').write('approved')\". Reply DONE afterward.")
		approvalMessage, ok := fake.WaitSend(time.Now().Add(60*time.Second), func(message fakeMessage) bool {
			return strings.Contains(message.Text, "Precisa de aprovação")
		})
		if !ok {
			t.Fatalf("E2E B approve: solicitação de permissão não chegou; artefatos: %s", artifactRoot)
		}
		approveButton, ok := findButton(approvalMessage, "Aprovar")
		if !ok {
			t.Fatalf("botão Aprovar ausente: %+v", approvalMessage.Keyboard)
		}
		fake.PushCallback(fakeChatID, approvalMessage.ID, "e2e-approve", approveButton.Data)
		outputApprove, err := waitSession(resultApprove, 90*time.Second)
		writeArtifact(t, artifactRoot, "scenario-b-approve.txt", []byte(outputApprove))
		if err != nil {
			t.Fatalf("E2E B approve: %v\n%s", err, outputApprove)
		}
		if _, err := os.Stat(filepath.Join(project, "logs", "permission-approved.txt")); err != nil {
			t.Fatalf("comando aprovado não criou o arquivo: %v\n%s", err, outputApprove)
		}
	}

	resultDeny := startDevin(t, devinExe, project, home, "Use exec to run exactly: python -c \"open('logs/permission-denied.txt','w').write('denied')\". If the phone denies it, follow the instruction and explain briefly.")
	denyMessage, ok := fake.WaitSend(time.Now().Add(60*time.Second), func(message fakeMessage) bool {
		return strings.Contains(message.Text, "Precisa de aprovação")
	})
	if !ok {
		t.Fatalf("E2E B deny: solicitação de permissão não chegou")
	}
	denyButton, ok := findButton(denyMessage, "Negar com instrução")
	if !ok {
		t.Fatalf("botão Negar com instrução ausente: %+v", denyMessage.Keyboard)
	}
	fake.PushCallback(fakeChatID, denyMessage.ID, "e2e-deny-instruction", denyButton.Data)
	instructionPrompt, ok := fake.WaitSend(time.Now().Add(15*time.Second), func(message fakeMessage) bool { return message.ForceReply })
	if !ok {
		t.Fatal("E2E B deny: ForceReply da instrução não chegou")
	}
	fake.PushMessage(fakeChatID, instructionPrompt.ID, "Não execute o comando; responda apenas que a ação não foi executada.")
	outputDeny, err := waitSession(resultDeny, 90*time.Second)
	writeArtifact(t, artifactRoot, "scenario-b-deny-instruction.txt", []byte(outputDeny))
	if err != nil {
		t.Fatalf("E2E B deny: %v\n%s", err, outputDeny)
	}
	if _, err := os.Stat(filepath.Join(project, "logs", "permission-denied.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("comando negado criou arquivo: err=%v", err)
	}
	assertFakeEditPreserves(t, fake, denyMessage.ID, denyMessage.Text, "Negado")
	assertFakeEditPreserves(t, fake, instructionPrompt.ID, instructionPrompt.Text, "Negado")

	drainFakeSends(fake)
	resultQuestion := startDevin(t, devinExe, project, home, "Chame a ferramenta ask_user_question, não faça a pergunta em texto: pergunte exatamente 'Qual é a cor do céu?' com as opções 'Azul' e 'Verde'. Depois responda com a opção escolhida.")
	questionMessage, ok := fake.WaitSend(time.Now().Add(60*time.Second), func(message fakeMessage) bool {
		return len(message.Keyboard) > 0 && !strings.Contains(message.Text, "Precisa de aprovação")
	})
	if !ok {
		output, err := waitSession(resultQuestion, 20*time.Second)
		writeArtifact(t, artifactRoot, "scenario-c-no-question.txt", []byte(output))
		t.Fatalf("E2E C: pergunta não chegou ao Telegram; err=%v output=%s; veja %s", err, output, artifactRoot)
	}
	green, ok := findButton(questionMessage, "Verde")
	if !ok {
		t.Fatalf("opção Verde ausente: %+v", questionMessage.Keyboard)
	}
	fake.PushCallback(fakeChatID, questionMessage.ID, "e2e-question-verde", green.Data)
	outputQuestion, err := waitSession(resultQuestion, 90*time.Second)
	writeArtifact(t, artifactRoot, "scenario-c-question.txt", []byte(outputQuestion))
	if err != nil || !strings.Contains(strings.ToLower(outputQuestion), "verde") {
		t.Fatalf("E2E C: resposta não usou Verde; err=%v output=%s", err, outputQuestion)
	}
	assertFakeEditPreserves(t, fake, questionMessage.ID, questionMessage.Text, "Verde")
	if focusedRework {
		if output, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "uninstall", "--agent", "claude", "--scope", "project", "--project-dir", project); err != nil {
			t.Fatalf("uninstall Claude: %v\n%s", err, output)
		}
		if output, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "uninstall", "--agent", "devin", "--scope", "project", "--project-dir", project); err != nil {
			t.Fatalf("uninstall Devin: %v\n%s", err, output)
		}
		assertJSONEquivalent(t, devinHooksPath, originalDevin)
		assertJSONEquivalent(t, claudeSettingsPath, originalClaude)
		if err := runBridgeStatusStop(artifactRoot, bridgeEnv, bridgeExe); err != nil {
			t.Fatalf("parar daemon E2E: %v", err)
		}
		bridgeLog, _ := os.ReadFile(config.LogPath(home))
		writeArtifact(t, artifactRoot, "agent-bridge.log", bridgeLog)
		if !strings.Contains(string(bridgeLog), "hook claude ignorado sob Devin") {
			t.Fatalf("guard Claude sob Devin não foi registrado; examine %s", artifactRoot)
		}
		t.Logf("artefatos E2E focados: %s", artifactRoot)
		return
	}

	resultOther := startDevin(t, devinExe, project, home, "Chame a ferramenta ask_user_question, não faça a pergunta em texto: pergunte exatamente 'Escolha a sobremesa' com as opções 'Bolo' e 'Fruta'. Responda com a opção escolhida.")
	otherQuestion, ok := fake.WaitSend(time.Now().Add(60*time.Second), func(message fakeMessage) bool {
		return strings.Contains(strings.ToLower(message.Text), "sobremesa") && len(message.Keyboard) > 0
	})
	if !ok {
		t.Fatal("E2E C Outro: pergunta não chegou ao Telegram")
	}
	otherButton, ok := findButton(otherQuestion, "Outro (texto)")
	if !ok {
		t.Fatalf("botão Outro (texto) ausente: %+v", otherQuestion.Keyboard)
	}
	fake.PushCallback(fakeChatID, otherQuestion.ID, "e2e-question-other", otherButton.Data)
	freeReply, ok := fake.WaitSend(time.Now().Add(15*time.Second), func(message fakeMessage) bool { return message.ForceReply })
	if !ok {
		t.Fatal("E2E C Outro: ForceReply não chegou ao Telegram")
	}
	fake.PushMessage(fakeChatID, freeReply.ID, "Sorvete")
	outputOther, err := waitSession(resultOther, 90*time.Second)
	writeArtifact(t, artifactRoot, "scenario-c-other-text.txt", []byte(outputOther))
	if err != nil || !strings.Contains(strings.ToLower(outputOther), "sorvete") {
		t.Fatalf("E2E C Outro: resposta livre não foi usada; err=%v output=%s", err, outputOther)
	}

	fake.SetResponder(nil)
	drainFakeSends(fake)
	interactive, err = startDevinPTY(devinExe, []string{"--model", "swe-2-medium", "--respect-workspace-trust", "false"}, cleanDevinEnvironment(os.Environ(), home), project)
	if err != nil {
		t.Fatalf("E2E D: iniciar Devin por ConPTY: %v", err)
	}
	if !interactive.WaitFor("Devin CLI", 30*time.Second) {
		writeArtifact(t, artifactRoot, "scenario-d-pty-start.txt", []byte(interactive.Output()))
		t.Fatalf("E2E D: TUI não iniciou; veja scenario-d-pty-start.txt")
	}
	time.Sleep(3 * time.Second)
	if err := interactive.SendLine("Responda apenas OK e encerre o turno."); err != nil {
		t.Fatalf("E2E D: enviar prompt inicial: %v", err)
	}
	lateStop, ok := fake.WaitSend(time.Now().Add(90*time.Second), func(message fakeMessage) bool {
		return strings.HasPrefix(message.Text, "[devin ·") && len(message.Keyboard) == 0
	})
	if !ok {
		writeArtifact(t, artifactRoot, "scenario-d-no-stop.txt", []byte(interactive.Output()))
		t.Fatalf("E2E D: Stop inicial não chegou ao Telegram; TUI=%q", interactive.Output())
	}
	if _, ok := fake.WaitEdit(time.Now().Add(15*time.Second), func(edit map[string]any) bool {
		messageID, _ := edit["message_id"].(float64)
		text, _ := edit["text"].(string)
		return int64(messageID) == lateStop.ID && strings.Contains(text, "Tempo esgotado")
	}); !ok {
		t.Fatalf("E2E D: Stop não expirou dentro do tempo configurado")
	}
	fake.PushMessage(fakeChatID, lateStop.ID, "Na próxima parada, responda com BANANA.")
	if _, ok := fake.WaitSend(time.Now().Add(10*time.Second), func(message fakeMessage) bool {
		return strings.Contains(message.Text, "Enfileirado; será entregue")
	}); !ok {
		t.Fatal("E2E D: resposta tardia não foi enfileirada")
	}
	time.Sleep(2 * time.Second)
	if err := interactive.SendLine("Continue e siga a resposta recebida pelo celular."); err != nil {
		t.Fatalf("E2E D: enviar segundo prompt: %v", err)
	}
	if !interactive.WaitFor("BANANA", 90*time.Second) {
		writeArtifact(t, artifactRoot, "scenario-d-late-reply.txt", []byte(interactive.Output()))
		t.Fatalf("E2E D: resposta enfileirada não chegou à próxima parada")
	}
	writeArtifact(t, artifactRoot, "scenario-d-late-reply.txt", []byte(interactive.Output()))
	interactive.Close()
	interactive = nil

	if output, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "away", "off"); err != nil {
		t.Fatalf("desativar modo ausente: %v\n%s", err, output)
	}
	drainFakeSends(fake)
	resultPresent := startDevin(t, devinExe, project, home, "Responda exatamente NOTIFY_PRESENT_7 e encerre o turno.")
	presentNotification, ok := fake.WaitSend(time.Now().Add(60*time.Second), func(message fakeMessage) bool {
		return strings.Contains(message.Text, "NOTIFY_PRESENT_7")
	})
	if !ok {
		t.Fatal("E2E E: notificação em modo presente não foi enviada")
	}
	outputPresent, err := waitSession(resultPresent, 60*time.Second)
	writeArtifact(t, artifactRoot, "scenario-e-present.txt", []byte(outputPresent))
	if err != nil {
		t.Fatalf("E2E E: %v\n%s", err, outputPresent)
	}
	if !strings.Contains(presentNotification.Text, "NOTIFY_PRESENT_7") {
		t.Fatalf("notificação não contém mensagem final: %q", presentNotification.Text)
	}
	allMessages, _ := fake.Snapshot()
	presentCount := 0
	for _, message := range allMessages {
		if strings.Contains(message.Text, "NOTIFY_PRESENT_7") {
			presentCount++
		}
	}
	if presentCount != 1 {
		t.Fatalf("esperava uma notificação em modo presente; recebeu %d", presentCount)
	}

	if output, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "uninstall", "--agent", "claude", "--scope", "project", "--project-dir", project); err != nil {
		t.Fatalf("uninstall Claude: %v\n%s", err, output)
	}
	if output, err := runBridge(artifactRoot, bridgeEnv, bridgeExe, "uninstall", "--agent", "devin", "--scope", "project", "--project-dir", project); err != nil {
		t.Fatalf("uninstall Devin: %v\n%s", err, output)
	}
	assertJSONEquivalent(t, devinHooksPath, originalDevin)
	assertJSONEquivalent(t, claudeSettingsPath, originalClaude)
	if err := runBridgeStatusStop(artifactRoot, bridgeEnv, bridgeExe); err != nil {
		t.Fatalf("parar daemon E2E: %v", err)
	}
	fakeMessages, fakeEdits := fake.Snapshot()
	data, _ := json.MarshalIndent(fakeMessages, "", "  ")
	writeArtifact(t, artifactRoot, "fake-telegram-messages.json", data)
	data, _ = json.MarshalIndent(fakeEdits, "", "  ")
	writeArtifact(t, artifactRoot, "fake-telegram-edits.json", data)
	data, _ = json.MarshalIndent(fake.UpdatesSnapshot(), "", "  ")
	writeArtifact(t, artifactRoot, "fake-telegram-updates.json", data)
	bridgeLog, _ := os.ReadFile(config.LogPath(home))
	writeArtifact(t, artifactRoot, "agent-bridge.log", bridgeLog)
	if !strings.Contains(string(bridgeLog), "hook claude ignorado sob Devin") {
		t.Fatalf("não houve evidência de carregamento do hook Claude sob Devin; examine %s", artifactRoot)
	}
	t.Logf("artefatos E2E: %s", artifactRoot)
}

type sessionResult struct {
	output string
	err    error
}

func startDevin(t *testing.T, executable, project, home, prompt string) <-chan sessionResult {
	t.Helper()
	result := make(chan sessionResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	go func() {
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-p", prompt, "--model", "swe-2-medium", "--respect-workspace-trust", "false")
		command.Dir = project
		command.Env = cleanDevinEnvironment(os.Environ(), home)
		output, err := command.CombinedOutput()
		result <- sessionResult{output: string(output), err: err}
	}()
	return result
}

func waitSession(resultChannel <-chan sessionResult, timeout time.Duration) (string, error) {
	select {
	case result := <-resultChannel:
		return result.output, result.err
	case <-time.After(timeout):
		return "", fmt.Errorf("Devin não encerrou em %s", timeout)
	}
}

func buildBridge(root, output string) (string, error) {
	goExe := os.Getenv("GO_EXE")
	if goExe == "" {
		goExe, _ = exec.LookPath("go")
	}
	if goExe == "" && runtime.GOOS == "windows" {
		goExe = `C:\Program Files\Go\bin\go.exe`
	}
	command := exec.Command(goExe, "build", "-o", output, "./cmd/agent-bridge")
	command.Dir = root
	data, err := command.CombinedOutput()
	return string(data), err
}

func runBridge(artifacts string, env []string, executable string, args ...string) (string, error) {
	command := exec.Command(executable, args...)
	command.Dir = artifacts
	command.Env = env
	data, err := command.CombinedOutput()
	return string(data), err
}

func runBridgeStatusStop(artifacts string, env []string, executable string) error {
	if output, err := runBridge(artifacts, env, executable, "daemon", "stop"); err != nil {
		return fmt.Errorf("parada do daemon: %w: %s", err, output)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := runBridge(artifacts, env, executable, "daemon", "status")
		if err == nil && strings.Contains(output, "Daemon inativo") {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("daemon continuou ativo após pedido de parada")
}

func cleanDevinEnvironment(environment []string, home string) []string {
	allowed := map[string]bool{}
	for _, name := range []string{"SYSTEMROOT", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH", "USERNAME", "TEMP", "TMP", "PATH", "COMPUTERNAME", "PROGRAMFILES", "PROGRAMDATA", "COMSPEC", "PATHEXT", "WINDIR", "SYSTEMDRIVE", "OS", "AGENT_BRIDGE_HOME"} {
		allowed[name] = true
	}
	result := make([]string, 0, len(allowed))
	for _, item := range environment {
		key, _, found := strings.Cut(item, "=")
		if found && allowed[strings.ToUpper(key)] {
			result = append(result, item)
		}
	}
	return setEnv(result, "AGENT_BRIDGE_HOME", home)
}

func setEnv(environment []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(strings.ToUpper(item), prefix) {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

func withHome(environment []string, home string) []string {
	return setEnv(environment, "AGENT_BRIDGE_HOME", home)
}

func runCommand(artifacts string, environment []string, executable string, args ...string) (string, error) {
	command := exec.Command(executable, args...)
	command.Dir = artifacts
	command.Env = environment
	data, err := command.CombinedOutput()
	return string(data), err
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller falhou")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func initializeGit(project string) error {
	command := exec.Command("git", "-C", project, "init")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, output)
	}
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func drainFakeSends(fake *fakeBot) {
	for {
		select {
		case <-fake.sent:
		default:
			return
		}
	}
}

func stopOnceResponder(fake *fakeBot, reply string) func(fakeMessage) {
	var mu sync.Mutex
	answered := map[string]bool{}
	return func(message fakeMessage) {
		if !strings.HasPrefix(message.Text, "[devin ·") || len(message.Keyboard) > 0 || message.ForceReply {
			return
		}
		header := strings.SplitN(message.Text, "\n", 2)[0]
		mu.Lock()
		if answered[header] {
			mu.Unlock()
			return
		}
		answered[header] = true
		mu.Unlock()
		fake.PushMessage(message.ChatID, message.ID, reply)
	}
}

func assertFakeEditPreserves(t *testing.T, fake *fakeBot, messageID int64, original, expected string) {
	t.Helper()
	edit, ok := fake.WaitEdit(time.Now().Add(10*time.Second), func(edit map[string]any) bool {
		id, _ := edit["message_id"].(float64)
		return int64(id) == messageID
	})
	if !ok {
		t.Fatalf("edição da mensagem %d não chegou ao bot fake", messageID)
	}
	text, _ := edit["text"].(string)
	if !strings.Contains(text, original) || !strings.Contains(text, expected) {
		t.Fatalf("edição não preservou o original %q ou status %q: %q", original, expected, text)
	}
}

func findButton(message fakeMessage, text string) (fakeButton, bool) {
	for _, row := range message.Keyboard {
		for _, button := range row {
			if button.Text == text {
				return button, true
			}
		}
	}
	return fakeButton{}, false
}

func writeArtifact(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertJSONEquivalent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("arquivo restaurado %s: %v", path, err)
	}
	var wantValue, gotValue any
	if err := json.Unmarshal(expected, &wantValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual, &gotValue); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(wantValue)
	gotJSON, _ := json.Marshal(gotValue)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("arquivo não restaurado semanticamente: %s\nantes=%s\ndepois=%s", path, wantJSON, gotJSON)
	}
}
