# portscout — design do painel

Data: 2026-09-09

Sucessor de `2026-09-09-portscout-design.md`, que descreve o portscout como
escritor do YAML do tunnel9. Aquele documento continua válido como registro do
que existia; este descreve o que o programa é.

## O que mudou, e por quê

O design anterior listava em *Não-objetivos*: "TUI própria — é o motivo de usar
o tunnel9" e "Abrir ou manter túneis. `portscout` nunca executa `ssh -L`".

**Os dois caíram.** Dois motivos, um de fora e um de dentro:

O de fora: o tunnel9 se mostrou quebrado em dois pontos que só aparecem em uso
real. Ele não expandia o `~` do `IdentityFile`, então nenhuma chave carregava e
o handshake não oferecia método algum; e escrevia a porta do SSH sobre a porta do
serviço, mandando todo forward para o sshd do remoto. Corrigido em
[sio2boss/tunnel9#8](https://github.com/sio2boss/tunnel9/pull/8), mas o episódio
respondeu a pergunta de dependência.

O de dentro: dois binários com um YAML no meio é atrito contra o que se quer
fazer. Você não sabe o que está rodando no remoto antes de olhar, e o fluxo
exigia escrever o mapeamento antes de olhar.

**Os outros dois continuam valendo:** sem daemon e sem transporte que não seja
SSH.

## Forma

```
portscout <ssh-host>          abre o painel e os túneis (padrão)
portscout list <ssh-host>     descobre e imprime, sem tunelar
portscout up <ssh-host>       igual ao padrão, para um host chamado "list"

flags: --all --every=10s --state=CAMINHO --no-autostart
```

Um host por sessão. Multi-host seria código especulativo: dois terminais
resolvem, e cada host teria estado de conexão e de falha próprios.

Primeiro plano, como o `htop`. Sair derruba os túneis; o que sobrevive são as
escolhas.

## O túnel é um ControlMaster

**Um único master do OpenSSH, dirigido por `-O forward` / `-O cancel` /
`-O check`.** Não um processo `ssh -L` por linha, e não SSH nativo em Go.

Medido contra um host real por Tailscale: o master leva 2,7s uma vez, e depois
disso um forward custa 4ms e um cancel 3ms. Um processo por túnel custaria um
handshake completo a cada toggle — e **o toggle é a interação central do
programa**. Um painel cuja gesto principal leva um segundo não é um painel.

SSH nativo em Go está descartado por evidência, não por gosto: `x/crypto/ssh`
não tem parser de `ssh_config` nenhum, e o tunnel9 morreu exatamente nisso.
`IdentityFile` é a parte rasa do problema — ainda haveria `Match`, tokens,
`ProxyCommand`, `known_hosts`, chaves FIDO.

Três regras tornam isso seguro em vez de esperto:

- `-M -S <nosso socket>` sempre explícitos, sobrepondo o `ControlMaster` do
  usuário. Não sequestramos a conexão dele nem somos quebrados por ela.
- `ControlPersist=no` faz "os túneis morrem com o painel" ser verdade por
  construção, e não por um `defer` que talvez não rode.
- Um master sobrevivente de execução morta **não é adotado**. Não existe comando
  de mux que enumere forwards existentes, então o primeiro `-O forward` viraria
  um erro de bind sobre o qual teríamos de mentir. Mata e recomeça.

E uma de higiene: `BatchMode=yes` com stdin nulo em toda invocação. Um filho que
resolve pedir passphrase brigaria com a TUI pelo terminal e deixaria a tela
irrecuperável.

## A UI lê snapshot, não stream

A UI consome `Snapshot()` mais uma campainha coalescente, e nunca um stream de
deltas.

Um stream obrigaria a UI a manter uma **réplica** do estado e aplicar diffs —
duas fontes de verdade, que divergem exatamente no caso para o qual o programa
existe: uma varredura remove a porta enquanto o túnel está conectando e um erro
dela ainda está em voo. Com snapshot, o `View` é função pura de um valor que ela
não calculou, e um teste monta qualquer tela com um literal.

Um tick de um segundo relê o snapshot incondicionalmente, o que torna a
campainha uma **otimização de latência, não requisito de correção**: se ela
falhar, a tela fica no máximo um segundo velha em vez de permanentemente errada.

`SetLocalPort` é a única chamada síncrona, de propósito: o erro dela é o que o
prompt de edição precisa mostrar.

## O layout é calculado no Update, nunca no View

`View` tem receiver por valor e não pode guardar o que desenhou. Qualquer
geometria decidida durante a renderização seria invisível para o hit-testing do
mouse, e as duas divergiriam em silêncio — clique numa linha, alterna outra.

Então o `Update` recomputa um `frame` (offset de scroll, Y da primeira linha,
faixas X do checkbox e da porta local) a cada resize, snapshot e movimento; o
`View` renderiza a partir dele; e o hit-testing lê o mesmo `frame`.

É também o motivo de não usar `bubbles/table`: o offset de scroll do viewport
dele é inacessível, então "clique na linha *k* da tela" precisaria de um contador
paralelo mantido à mão — exatamente a segunda fonte de verdade que este design
evita em todo lugar.

Duas regras caem do modelo de colunas:

- **O checkbox carrega o estado em qualquer largura.** A palavra por extenso é o
  primeiro detalhe a cair quando estreita, e é por isso que o tier de 60 colunas
  não perde informação.
- **Só ASCII dentro das colunas alinhadas.** `->`, não `→`; `[x]`, não `●`. Setas
  e bolinhas são de largura *ambígua* em East-Asian: o `go-runewidth` conta uma
  célula, um terminal configurado como wide desenha duas, e toda coluna à direita
  anda.

## Estado

`encoding/json`, **um arquivo por host** em
`$XDG_STATE_HOME/portscout/hosts/<host>.json`.

Um arquivo por host, e não um mapa: dois painéis em dois terminais fariam
read-modify-write concorrente e cada um descartaria a escolha do outro em
silêncio.

JSON e não YAML: a única vantagem real do YAML era round-trip de comentários num
arquivo editado à mão, e o painel passa a ser a superfície de edição. Isso apaga
as ~370 linhas de ginástica com `yaml.Node` do pacote antigo.

Só o que carrega decisão do usuário é gravado (desejado ou porta fixada). Uma
linha que ninguém tocou é derivável da próxima varredura, então nunca chega no
arquivo — e por isso ele se autolimpa e dispensa um `--prune`.

## A porta local é decidida uma vez

`plan.Build`, que recalculava o conjunto inteiro a cada chamada, foi substituído
por `plan.Assign`, que decide uma porta.

Sob um painel vivo, recalcular era **ativamente nocivo**: a cada varredura as
portas que o próprio portscout tivesse ligado responderiam ocupadas, e ele daria
alegremente uma porta local nova a cada túnel em uso.

A invariante: *a porta local de uma linha é decidida quando a linha aparece, e
então congelada.* Para porta fixada pelo usuário, o `Assign` nunca é consultado —
remapear em silêncio um número que a pessoa digitou é o que faz uma ferramenta
parecer desonesta.

## Estrutura

```
cmd/portscout/     dispatch e a fiação do painel
internal/sshmux/   o binário ssh(1) como API tipada
internal/tunnel/   supervisor: desejado vs. real, por forward
internal/state/    escolhas persistidas
internal/session/  o painel inteiro, sem terminal
internal/ui/       Bubble Tea sobre o snapshot
internal/discover/ ss + docker ps numa sessão SSH
internal/plan/     porta local e nomes
```

Dependências em mão única: `ui → session → {tunnel, state, plan, discover}` e
`tunnel → sshmux`.

O `internal/session` é a peça que talvez surpreenda: **a aplicação inteira sem
terminal nenhum.** É ele que permite testar o comportamento de ponta a ponta sem
TTY, e é onde moram as invariantes que importam.

## Comportamento sob falha

| Situação | Comportamento |
| --- | --- |
| Varredura falhou | **nada muda** — nenhuma linha some, nenhum checkbox cai, nenhum túnel cai |
| Porta sumiu do remoto | a linha fica e o túnel fica; ela só aparece apagada |
| Porta local ocupada | linha em erro e a intenção é limpa, sem retry |
| Link caiu | linhas desejadas voltam a "conectando", backoff de 1s a 30s, replay ao voltar |
| Chave recusada / host key | fatal, sem retry |
| Arquivo de estado ilegível | começa vazio com aviso não fatal |
| `SIGKILL` no portscout | o master morre junto; a abertura seguinte limpa o socket órfão |

Duas dessas merecem o porquê:

**"Varredura falhou: nada muda"** é a regra mais importante do programa. Uma
chamada SSH instável não pode custar as escolhas do usuário. Ela tem teste com
nome próprio.

**Bind ocupado não retenta.** Uma porta ocupada não se libera sozinha, e
retentar a cada segundo produz um fluxo de erros idênticos que enterra a única
mensagem que a pessoa precisa ler.

## Testes

**Nenhum teste abre ssh, rede ou terminal.** As costuras: `sshmux.Client` e
`session.Forwarder` são interfaces trocadas inteiras; `discover.Runner` já era;
`portFree`, `Now`, `Tick`, `SaveDelay` e `After` são injetados; e o `Update` do
Bubble Tea é função pura, então as telas são asseridas sobre o texto renderizado
sem nunca construir um `tea.Program`.

O CI roda com `-race`: com quatro tipos de goroutine, deixou de ser opcional.

O que os testes puros **não** pegam: descobri três defeitos de layout apenas
rodando o painel contra o host real dentro de um emulador de terminal e lendo o
buffer renderizado. Um cabeçalho truncado, um aviso vazando sobre linhas
invisíveis, e um aviso grudento. Vale repetir esse ritual a cada mudança de
tela.
