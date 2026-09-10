# portscout

Descobre as portas em LISTEN numa máquina remota e encaminha as que você
escolher, num painel de terminal.

```sh
portscout dev
```

`dev` é qualquer destino que o seu `ssh` entenda — o `~/.ssh/config` é a fonte
de máquinas.

```
 portscout · dev                                          6/6 ativos · varredura há 3s

      ESTADO     LOCAL    REMOTA NOME
> [x] ativa       3102  ->  3100 next-server (v16.3.4)
  [x] ativa       3103  ->  3101 MainThread
  [x] ativa       9090* ->  3102 couple-community-postgres-1
  [x] ativa       3105  ->  3103 couple-community-redis-1
  [!] erro        3106  ->  3104 couple-community-mailpit-1 (3104)
  [ ] off         3107  ->  3105 couple-community-mailpit-1 (3105)
 erro 3104: porta local 3106 já está em uso — troque com 'e'
 espaço liga/desliga · e porta local · a tudo · r varrer · ? ajuda · q sair
```

## Por quê

O VSCode Remote descobre as portas abertas no remoto e oferece encaminhá-las.
Nenhuma ferramenta de terminal faz a parte da descoberta — e as que fazem o
túnel querem o mapeamento escrito à mão, num arquivo, antes de você saber o que
está rodando lá. Isto junta as duas metades: abre, mostra o que existe, e você
liga o que interessa.

## Uso

| tecla | efeito |
| --- | --- |
| `↑ ↓` / `k j` | mover |
| `espaço`, `enter` | liga/desliga a linha |
| `e` | editar a porta local |
| `a` | liga/desliga tudo que está visível |
| `t` | mostrar também as portas de sistema |
| `r`, `F5` | varrer agora |
| `?` | ajuda |
| `q`, `ctrl+c` | sair — **derruba os túneis** |

E com o mouse: clique no `[ ]` liga/desliga aquela linha, clique na linha só
seleciona, clique duplo liga/desliga, clique duplo na porta local abre a
edição, e a roda move a seleção. Com o mouse capturado, a seleção de texto do
terminal sai com `shift` pressionado.

Clique simples fora do `[ ]` nunca liga nem desliga. Um clique errado numa linha
larga mataria um túnel em uso, e o motivo mais comum de clicar numa linha é ler
o erro dela.

Também existe a saída em texto, útil em script e para conferir a descoberta sem
tunelar nada:

```sh
portscout list dev
```

## O que ele descobre

Une `ss -ltnpH` (processos do host) com `docker ps` (containers) numa única
sessão SSH.

As duas fontes são necessárias: com Docker rootless, **toda** porta publicada
aparece no `ss` como o processo `rootlesskit`, sem identificar o container. O
nome utilizável só vem do `docker ps`, casado pela porta do host.

Portas de sistema — `sshd`, `systemd-resolved`, 22, 53, 123… — ficam escondidas.
`t` mostra tudo, e uma porta que você já escolheu nunca é escondida, mesmo que
pareça ruído.

## Os nomes das linhas

O `ss` só mostra os 15 primeiros caracteres do nome de um processo, que é o
limite do kernel — `next-server (v16.3.4)` chega como `next-server (v1`. O nome
inteiro é lido do `/proc/<pid>/cmdline`, e só é usado quando de fato continua o
nome truncado.

Um container que publica mais de uma porta repete o mesmo nome em cada uma. Só
os nomes que se repetem ganham a porta remota como sufixo — `mailpit-1 (3104)` e
`mailpit-1 (3105)` — para não haver duas linhas idênticas.

## As portas locais

O padrão é usar o mesmo número da porta remota, que é o que você quer dizer.
Quando ele está ocupado — por outra linha ou por algo escutando na sua máquina —
a próxima livre é escolhida e o painel avisa.

A porta local de uma linha é decidida uma vez, quando a linha aparece, e então
congelada. Uma varredura nunca move um túnel em uso.

Se você digitar uma porta com `e`, ela é marcada com `*` e passa a ser verbatim:
nunca é remapeada, e volta igual na próxima abertura.

## Estado

As suas escolhas — o que tunelar e onde — ficam em
`$XDG_STATE_HOME/portscout/hosts/<host>.json` e sobem sozinhas na próxima
abertura. Um arquivo por host, para dois painéis em dois terminais não
sobrescreverem as escolhas um do outro.

Só o que carrega decisão sua é gravado, então o arquivo se limpa sozinho quando
você desliga algo.

## Ciclo de vida

O painel roda em primeiro plano, como o `htop`. **Sair derruba os túneis** — não
há daemon, nem processo sobrevivente. O que sobrevive são as escolhas.

Por baixo, tudo anda sobre um único `ControlMaster` do OpenSSH: uma
autenticação por sessão, e depois disso ligar ou desligar uma porta custa uns
poucos milissegundos em vez de um handshake inteiro. A varredura pega carona no
mesmo socket.

Delegar ao binário `ssh` é deliberado: é o que faz o seu `~/.ssh/config` valer
inteiro — `ProxyCommand`, `IdentitiesOnly`, chaves FIDO, `Match`, tudo.

## Quando algo dá errado

| situação | comportamento |
| --- | --- |
| Porta local ocupada | linha em erro, sem retry — troque com `e` |
| Serviço sumiu do remoto | a linha fica, o túnel fica; ela só aparece apagada |
| Varredura falhou | nada muda; só o aviso no topo |
| Conexão caiu | as linhas voltam para "conectando" e sobem de novo sozinhas |
| Chave recusada | erro fatal, sem martelar o sshd — rode `ssh <host>` uma vez |

## Instalação

```sh
go install github.com/MarllonGomes/portscout/cmd/portscout@latest
```

Ou baixe o binário da página de releases. Precisa do `ssh` no PATH; nada mais.

## Flags

Podem vir antes ou depois do host.

| Flag | Padrão | Efeito |
| --- | --- | --- |
| `--all` | `false` | Já abrir mostrando as portas de sistema |
| `--every` | `10s` | Intervalo entre varreduras |
| `--state` | padrão do XDG | Caminho do arquivo de escolhas |
| `--no-autostart` | `false` | Não subir os túneis lembrados na abertura |

## Desenvolvimento

```sh
go test -race ./...   # nenhum teste abre ssh, rede ou terminal
go vet ./...
```

O `internal/session` é o painel inteiro sem terminal nenhum: é ele que permite
testar o comportamento de ponta a ponta sem TTY.
