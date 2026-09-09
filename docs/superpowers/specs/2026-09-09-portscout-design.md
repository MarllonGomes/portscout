# portscout — design

Data: 2026-09-09

## Problema

Alcançar, do PC local, uma porta que só escuta em loopback numa máquina remota de
desenvolvimento. O VSCode Remote resolve isso descobrindo as portas abertas no remoto e
oferecendo encaminhá-las; nenhuma ferramenta de terminal faz a parte da descoberta.

O [tunnel9](https://github.com/sio2boss/tunnel9) já entrega TUI, ligar/desligar túnel,
monitoramento de throughput e latência, remapeamento de porta local e persistência em YAML.
O que falta nele é saber **o que existe** no remoto: os túneis são escritos à mão.

`portscout` preenche só essa lacuna. Descobre as portas do remoto e escreve as entradas no
YAML do tunnel9. A UI, o transporte e o monitoramento continuam sendo do tunnel9.

## Não-objetivos

Deliberadamente fora de escopo, para o programa continuar pequeno:

- TUI própria — é o motivo de usar o tunnel9.
- Abrir ou manter túneis. `portscout` nunca executa `ssh -L`.
- Daemon, serviço de sistema, autostart.
- Transporte que não seja SSH.

## Forma

Binário Go único, executado no cliente (o PC do usuário), com três subcomandos:

```
portscout list  <ssh-host>                 # descobre e imprime; não escreve nada
portscout scan  <ssh-host>                 # descobre e faz merge no YAML do tunnel9
portscout watch <ssh-host> [--every 15s]   # scan em loop
```

`<ssh-host>` é qualquer destino que o `ssh` aceite, então `~/.ssh/config` é a fonte de
máquinas. `portscout` não tem cadastro de hosts próprio.

Flags: `--all` (não filtrar ruído de sistema), `--tag` (tag gerenciada, padrão `auto`),
`--config` (caminho do YAML, mesma precedência do tunnel9), `--prune`, `--dry-run`.

## Descoberta

Uma única sessão SSH por varredura, executando um snippet POSIX que emite duas listas:

1. `ss -ltnpH` — sockets TCP em LISTEN, com processo e pid.
2. `docker ps --format '{{json .}}'` — mapeia porta publicada no host → nome do container.

As duas fontes são necessárias. Com Docker rootless, toda porta publicada aparece no `ss`
como o processo `rootlesskit`, sem identificar o container; o nome utilizável vem do
`docker ps`, casado pela porta do host.

Degradações previstas:

- Sem `docker` no remoto: segue só com `ss`, com aviso. Não é erro.
- `docker` presente mas socket ausente: tenta `DOCKER_HOST=unix://$HOME/.docker/run/docker.sock`
  antes de desistir (layout do rootless).
- `0.0.0.0` e `::` na mesma porta: colapsa em uma linha, registrando o bind.

Ruído de sistema (sshd, systemd-resolved, chrony, cups, avahi) é omitido por padrão e volta
com `--all`.

## Merge no YAML

O arquivo é lido como `yaml.Node` (yaml.v3), não desserializado em struct. Isso preserva
comentários, ordem das chaves e campos que o `portscout` não conhece.

Regra de propriedade: **o portscout só é dono das entradas marcadas com `tag: auto`.** O
resto do arquivo nunca é tocado.

Chave de identidade: `(host, remote_port)`.

| Situação | Comportamento |
| --- | --- |
| Porta descoberta, sem entrada | Cria entrada, `local_port = remote_port`, `tag: auto`, alias do container/processo |
| Porta descoberta, entrada `auto` existe | Preserva `local_port` editado pelo usuário; atualiza só o alias |
| Entrada perdeu a `tag: auto` | Passa a ser do usuário; portscout ignora |
| Entrada `auto` cuja porta sumiu | Mantida por padrão; removida apenas com `--prune` |

Entradas que sumiram não são apagadas por padrão porque apagar derruba um túnel que pode
estar em uso.

### Colisão de porta local

`local_port = remote_port` é o padrão pedido, mas o PC local também roda containers. Antes
de gravar, o portscout verifica o que escuta localmente e as demais entradas do arquivo; em
conflito, escolhe a próxima porta livre e **informa o remapeamento na saída**. Silenciar um
remap seria pior que a colisão.

### Escrita

Arquivo temporário e `rename` atômico, com backup na primeira escrita. Se o YAML não
parsear, aborta sem escrever nada.

## Estrutura

```
cmd/portscout/          entrypoint e flags
internal/discover/      snippet remoto, parsing de ss e docker ps
internal/tunnel9/       load, merge e save do YAML
internal/plan/          nomes de alias, resolução de colisão
testdata/               fixtures golden
```

## Testes

A descoberta fica atrás de uma interface `Runner` que devolve saída crua; os testes usam um
fake e **nenhum teste abre SSH**.

- `discover`: fixtures golden de `ss` e `docker ps`, incluindo o caso rootless onde todas as
  portas são `rootlesskit`, o colapso `0.0.0.0`/`::` e a ausência de docker.
- `tunnel9`: table-driven — entrada nova, `local_port` preservado, entrada sem tag intocada,
  `--prune`, comentários e ordem preservados, YAML inválido aborta sem escrever.
- `plan`: colisão local, colisão entre entradas, escolha da próxima porta livre.

## Erros

| Falha | Comportamento |
| --- | --- |
| SSH falha | Sai não-zero com a mensagem do ssh; YAML intocado |
| YAML não parseia | Aborta sem escrever; sugere `--config` |
| Sem docker no remoto | Aviso, segue com `ss` |
| Nenhuma porta encontrada | Sai zero, informa que não havia nada |

## Distribuição

Repositório privado no GitHub. Release com goreleaser (cross-compile Linux/macOS, amd64 e
arm64) e `go install` como alternativa.
