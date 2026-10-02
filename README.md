# agent-bridge

Ponte local, bidirecional, entre hooks de ciclo de vida de agentes de programação e um bot do Telegram. O agente continua sendo executado no computador; o daemon local entrega notificações e decisões ao celular.

## Privacidade e segurança

Em modo ausente, mensagens finais completas do agente podem ser enviadas ao Telegram. Perguntas, resumos de comandos e respostas do celular também passam pelo bot configurado. Use um bot privado e revise o que será encaminhado antes de ativar o modo ausente. O bot só aceita atualizações do `chat_id` configurado. O daemon escuta apenas em `127.0.0.1` e exige um token aleatório local em todas as requisições. O token do BotFather fica em `config.json` com permissões restritas; ele nunca deve aparecer nos logs.

## Instalação

Baixe o arquivo correspondente ao seu sistema na página **Releases** do projeto e coloque `agent-bridge` em um diretório permanente no `PATH`. No Windows, use o executável `agent-bridge.exe`. Evite pastas temporárias e Downloads: os comandos dos hooks guardam o caminho absoluto do executável.

Para compilar a partir do código-fonte, instale Go 1.27 ou compatível e execute:

```sh
go build -o agent-bridge ./cmd/agent-bridge
```

## Configurar o Telegram

1. No Telegram, converse com `@BotFather`, crie um bot e copie o token.
2. Execute `agent-bridge setup telegram`. O token é solicitado sem eco na tela.
3. Envie `/start` em uma conversa privada com o bot dentro de dois minutos. O programa captura o `chat_id`, salva a configuração e envia uma confirmação.

## Configurar o Discord

1. No Discord Developer Portal, crie uma aplicação e um bot; habilite **Message Content Intent** nas opções do bot.
2. Execute `agent-bridge setup discord`. O token é solicitado sem eco na tela. Informe também seu ID de usuário, que pode ser copiado em Configurações > Avançado > Modo desenvolvedor, clicando no seu nome.
3. Convide o bot. Depois do próximo início do daemon, execute `/vincular computador:<nome>` no canal de texto desejado. O nome é o `machine_name` configurado ou o hostname.

Somente o ID informado durante a configuração pode vincular o computador, responder a solicitações e usar os comandos do bot. Cada computador vincula seu daemon ao canal de sua escolha; cada sessão aparece em uma thread. Threads são visíveis a qualquer pessoa que possa ver o canal, então prefira um canal privado. Quando Telegram e Discord estão configurados juntos, as solicitações vão aos dois; a primeira resposta vence e as mensagens nos outros canais são atualizadas com a origem da resposta. Comandos disponíveis no Discord: `/away`, `/back`, `/status`, `/list` e `/vincular`.

### Painel de andamento

O Discord mostra um painel por turno na thread da sessão, com contagens de ferramentas e falhas. O painel aparece sempre, tanto no modo presente quanto no ausente; o Telegram não o recebe. Configure `progress_detail` em `config.json` como `resumido` (padrão) ou `completo`; a opção completa inclui comandos e nomes de arquivos, portanto evite usá-la em computadores da empresa.

Execute `agent-bridge install` novamente para adicionar o hook `PostToolUse`, que atualiza o painel após cada ferramenta.

O daemon começa automaticamente quando um hook precisa dele. Também pode ser controlado manualmente:

```sh
agent-bridge daemon start
agent-bridge daemon status
agent-bridge daemon stop
agent-bridge test
```

### Início automático

Ao instalar hooks, `agent-bridge install` também registra o daemon para iniciar na próxima entrada do usuário. Consulte, habilite ou desabilite essa opção com `agent-bridge autostart status`, `agent-bridge autostart on` e `agent-bridge autostart off`. O registro fica no Run do usuário no Windows, em `~/Library/LaunchAgents/io.github.brbbruno.agent-bridge.plist` no macOS e em `$XDG_CONFIG_HOME/autostart/agent-bridge.desktop` (ou `~/.config/autostart/agent-bridge.desktop`) no Linux. No Windows, uma janela de console pode aparecer rapidamente durante a entrada.

Se `AGENT_BRIDGE_HOME` estiver definido, a instalação de hooks não ativa o início automático; remova a variável e execute `agent-bridge autostart on`. Para evitar que mensagens antigas sejam processadas muito depois de uma parada do daemon, mensagens do Telegram com mais de dez minutos são ignoradas e o bot informa quantas foram descartadas.

A configuração fica em `<UserConfigDir>/agent-bridge/config.json`, ou em `AGENT_BRIDGE_HOME/config.json` quando essa variável está definida. Por padrão, o diretório é `%APPDATA%\agent-bridge` no Windows, `~/Library/Application Support/agent-bridge` no macOS e `~/.config/agent-bridge` no Linux (respeitando `XDG_CONFIG_HOME`, quando definido). Se editar `config.json` manualmente, execute `agent-bridge daemon stop`; o próximo hook iniciará o daemon novamente com a configuração atualizada. `port` usa `47821` por padrão; `telegram.api_base` pode apontar para um servidor Bot API compatível em testes.

### Identificação das sessões

As mensagens usam um cabeçalho em duas linhas com o agente, o computador e o projeto, seguido do título da sessão e seu ID:

```
Devin · BRUNO-PC · agent-bridge
Sessão: Corrigir login (possible-celestite)
```

Para Devin, o título vem do CLI; se não estiver disponível, é usado o primeiro prompt da sessão. Configure `machine_name` para definir um apelido para o computador (o padrão é o hostname) e `devin_exe` para informar o caminho do CLI do Devin quando `devin` não estiver no `PATH`.

Cada computador precisa atualmente de seu próprio bot: dois daemons consultando o mesmo bot entram em conflito ao usar `getUpdates`. O suporte a vários computadores no mesmo bot está planejado.

## Instalar hooks

Para instalar hooks do usuário:

```sh
agent-bridge install --agent devin --scope user
agent-bridge install --agent claude --scope user
```

Para instalar no repositório atual:

```sh
agent-bridge install --agent devin --scope project
agent-bridge install --agent claude --scope project
```

Use `--project-dir DIR` para escolher outro repositório. A instalação mescla as entradas existentes, cria um backup com data/hora antes de alterar um arquivo e pode ser repetida. Para remover apenas as entradas do agent-bridge:

```sh
agent-bridge uninstall --agent devin --scope project
agent-bridge uninstall --agent claude --scope project
```

No Windows, o instalador converte o caminho do executável para barras (`C:/...`). Se ele contiver apenas letras, números, `_`, `.`, `/`, `:` e `-`, o comando fica sem aspas; essa forma funciona no Git Bash e no PowerShell. Caminhos com espaços ou outros caracteres especiais continuam entre aspas. Os hooks do Devin CLI usam Git Bash; Claude Code sem Git Bash pode recorrer ao PowerShell, onde a forma citada não é executável. Nesse caso, instale o executável em um caminho simples, sem espaços/caracteres especiais, e reinstale os hooks. Se o caminho estiver em uma pasta temporária ou Downloads, o programa avisa para movê-lo e reinstalar.

Quando um hook `--agent claude` é executado dentro do Devin, o agent-bridge não o processa novamente: `DEVIN_PROJECT_DIR` é o marcador positivo do harness Devin. Isso evita processamento duplicado quando o Devin também lê `.claude/settings.json`.

## Modo ausente

No computador:

```sh
agent-bridge away on
agent-bridge away off
agent-bridge away status
```

No Telegram, use `/away` e `/back`. Ative `/away` antes de sair do computador. Com o modo ausente ativo:

- **Stop:** envia a mensagem final completa e espera uma resposta por até `stop_wait` (8 horas por padrão). A sessão fica parada e ocupada no computador enquanto aguarda; a resposta do celular volta ao agente como instrução para continuar.
- **Permissão:** oferece **Aprovar**, **Negar** e **Negar com instrução** e aguarda por até `permission_wait` (8 horas por padrão). Se o tempo acabar, a decisão volta ao computador.
- **Pergunta:** apresenta cada questão e suas opções; permite selecionar várias opções quando aplicável ou responder em texto. Aguarda por até `question_wait` (8 horas por padrão); ao expirar, o agente é instruído a repetir as perguntas em texto no fim do turno.
- **Resposta tardia:** uma resposta a uma solicitação expirada é enfileirada para a próxima parada da mesma sessão.
- **Contexto do prompt:** avisa ao agente que o usuário está acompanhando pelo celular.

Use `/back` no Telegram ou `agent-bridge away off` no computador para liberar imediatamente todas as esperas em andamento. Se o turno já terminou sem modo ausente, a sessão não pode ser acordada pelo celular: a mensagem fica na fila e o bot informa que será entregue quando a sessão voltar a rodar. Para conversar pelo celular, ative `/away` antes de sair.

Com o modo ausente desligado, o bridge envia notificações sem bloquear o agente. `notify_when_present: false` desativa essas notificações.

### Uso no dia a dia

Antes de sair do computador, execute `agent-bridge away on`. Responda pelo celular às mensagens do bot; quando voltar, use `/back` no Telegram ou `agent-bridge away off` no computador.

Comandos disponíveis no Telegram: `/list`, `/s <sessão> <texto>`, `/status`, `/away`, `/back` e `/help`. Uma resposta vinculada a uma mensagem do bot é encaminhada à sessão correspondente; quando várias sessões aguardam, uma resposta sem vínculo pede que a sessão seja escolhida.

## Limites conhecidos

- Claude Code pode continuar um Stop até oito vezes consecutivas antes de encerrar o turno.
- Os formatos Claude Code são unitariamente testados, mas Claude Code não está instalado nesta máquina para um teste real.
- A execução de hooks do Devin Desktop não foi verificada neste ambiente; o fluxo real foi exercitado com Devin CLI.
- Processos iniciados pelo Devin Desktop podem herdar `ACP_BACKEND`, que fez o Devin CLI retornar “Not logged in” nos testes. O agent-bridge remove essa variável ao iniciar o daemon; os testes com Devin usam um ambiente limpo.
- O daemon é local; não exponha sua porta fora de `127.0.0.1`.

## Desenvolvimento

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
```

Os testes E2E reais do Devin no Windows são opcionais e só rodam com `AGENT_BRIDGE_E2E=1`; eles usam `swe-2-medium` e um servidor Bot API falso local. Os artefatos ficam em `artifacts/`.
